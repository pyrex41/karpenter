\* ===================================================================
   disruption.shen — the decision side of karpenter's disruption methods.

   Pure functions over a pre-digested snapshot (the plan's (disrupt ...)
   contract): candidate gating, method classification + priority, per-pool
   budget arithmetic, and Balanced scoring. Output is a list of
   disrupt-commands (decision-id + method) that the Go shell seeds into the
   command ledger (shen/types/command.shen) as pending entries. No k8s I/O,
   no wall-clock reads, no cron parsing — the shell pre-computes active
   budget windows and passes them in.

   Faithfulness to pkg/controllers/disruption (branch shen-core):
   - gate mirrors ValidateNodeDisruptable + ValidatePodsDisruptable
     (statenode.go:212-264), in the same check order.
   - classify mirrors the method ShouldDisrupt predicates and the
     controller's method order Emptiness -> StaticDrift -> Drift
     (controller.go:104-118); consolidation is deferred to Phase 4.
   - budget-allowed mirrors Budget.GetAllowedDisruptions
     (nodepool.go:382-404): int-or-percent, rounding UP.
   - THE BUG FIX: where MustGetAllowedDisruptions (nodepool.go:355-361)
     swallows a budget parse error and silently returns 0, here a parse
     error still fails CLOSED (0 allowed) but is carried out as a
     budget-report value so the shell can log/event it.
   - score-move / approved? mirror ScoreMove + ScoreResult.Approved
     (balanced.go:104-121, types.go:99-111): approved when
     (savings/poolCost)/(disruption/poolDisruption) >= 1/K, K=2.

   Loaded under the external (tc +) gate (make shen-check); every define
   carries a {..} signature. See shen/types/command.shen for why the gate
   is external (the loader samples the flag once at load start).
   =================================================================== *\

\* ---- candidate gating ---- *\

(datatype block-reason
  ________________ not-initialized : block-reason;
  ________________ marked-for-deletion : block-reason;
  ________________ nominated : block-reason;
  ________________ do-not-disrupt : block-reason;
  ________________ no-nodepool : block-reason;
  ________________ pods-blocked : block-reason;)

(datatype gate-result
  ________________ eligible : gate-result;
  R : block-reason;
  ==================
  [blocked R] : gate-result;)

(datatype method
  ________________ m-emptiness : method;
  ________________ m-staticdrift : method;
  ________________ m-drift : method;
  ________________ m-none : method;)

(datatype candidate
  Id : string; Np : string; Static : boolean; Init : boolean;
  Marked : boolean; Nom : boolean; Dnd : boolean; PB : boolean;
  Empty : boolean; Cons : boolean; Drift : boolean;
  Price : number; RCost : number;
  ================================================================
  [candidate Id Np Static Init Marked Nom Dnd PB Empty Cons Drift Price RCost] : candidate;)

\* gate: mirrors ValidateNodeDisruptable + ValidatePodsDisruptable, in the same
   order the Go code checks, returning the first blocking reason. *\
(define gate
  {candidate --> gate-result}
  [candidate _ Np _ Init Marked Nom Dnd PB _ _ _ _ _]
    -> (if (not Init) [blocked not-initialized]
       (if Marked [blocked marked-for-deletion]
       (if Nom [blocked nominated]
       (if Dnd [blocked do-not-disrupt]
       (if (= Np "") [blocked no-nodepool]
       (if PB [blocked pods-blocked]
       eligible)))))))

(define eligible?
  {candidate --> boolean}
  C -> (= (gate C) eligible))

\* ---- method classification + priority (Emptiness > StaticDrift > Drift) ---- *\

\* classify: the highest-priority method that applies to an already-eligible
   candidate. Emptiness excludes static NodePools (Emptiness.ShouldDisrupt);
   StaticDrift is drift on a static NodePool, Drift is drift on a standard one. *\
(define classify
  {candidate --> method}
  [candidate _ _ Static _ _ _ _ _ Empty Cons Drift _ _]
    -> (if (and (not Static) (and Empty Cons)) m-emptiness
       (if (and Static Drift) m-staticdrift
       (if (and (not Static) Drift) m-drift
       m-none))))

\* ---- budget arithmetic (int-or-percent, round up, errors as values) ---- *\

(datatype budget-nodes
  N : number;
  ================
  [count N] : budget-nodes;
  P : number;
  ================
  [percent P] : budget-nodes;
  Raw : string;
  ================
  [malformed Raw] : budget-nodes;)

(datatype budget-eval
  N : number;
  =================
  [allowed N] : budget-eval;
  Msg : string;
  =================
  [budget-error Msg] : budget-eval;)

\* nmod: A modulo B for non-negative A and positive B. The kernel exposes no
   typed mod/floor/ceiling (shen.mod is untyped under tc+), so we roll one from
   typed arithmetic. B is 100 or a small node count and A <= 100*numNodes, so the
   subtraction recursion is cheap at disruption's cardinality. *\
(define nmod
  {number --> number --> number}
  A B -> A where (< A B)
  A B -> (nmod (- A B) B))

\* ceil-div: ceil(A/B) for non-negative A, positive B, WITHOUT a floor/ceiling
   builtin. (A - (A mod B)) is an exact multiple of B, so its division is exact.
   Percentage budgets thus round UP exactly like
   intstr.GetScaledValueFromIntOrPercent(..., roundUp=true). *\
(define ceil-div
  {number --> number --> number}
  A B -> (let R (nmod A B)
           (if (> R 0) (+ (/ (- A R) B) 1) (/ (- A R) B))))

(define budget-allowed
  {budget-nodes --> number --> budget-eval}
  [count N] _ -> [budget-error "budget node count is negative"] where (< N 0)
  [count N] _ -> [allowed N]
  [percent P] _ -> [budget-error "budget percentage out of range"] where (or (< P 0) (> P 100))
  [percent P] Num -> [allowed (ceil-div (* P Num) 100)]
  [malformed Raw] _ -> [budget-error (cn "invalid disruption budget value: " Raw)])

\* ---- Balanced scoring: (savings/poolCost)/(disruption/poolDisruption) >= 1/K ---- *\

(datatype score-result
  SF : number; DF : number; K : number;
  =====================================
  [score SF DF K] : score-result;)

(define score-move
  {number --> number --> number --> number --> number --> score-result}
  Sav Disr TCost TDisr K
    -> (if (or (<= TCost 0) (<= TDisr 0))
           [score 0 0 K]
           [score (/ Sav TCost) (/ Disr TDisr) K]))

\* approved?: >= threshold 1/K. Non-positive savings never approve; zero
   disruption always approves (Go returns +Inf there); avoids representing Inf. *\
(define approved?
  {score-result --> boolean}
  [score SF DF K] -> (if (<= SF 0) false
                        (if (= DF 0) true
                          (>= (/ SF DF) (/ 1 K)))))

\* move-approved?: score-move then approved? in one call. The intermediate
   ScoreResult carries fractional (float) fields that the int-only sexpr
   contract cannot represent, so callers cross the boundary with the boolean
   verdict, never the raw score. *\
(define move-approved?
  {number --> number --> number --> number --> number --> boolean}
  Sav Disr TCost TDisr K -> (approved? (score-move Sav Disr TCost TDisr K)))

\* ---- top-level planner: gating + priority + budget -> commands ---- *\

(datatype pool-budget
  Np : string; Nodes : budget-nodes; NumNodes : number; Disrupting : number;
  =========================================================================
  [pool-budget Np Nodes NumNodes Disrupting] : pool-budget;)

(datatype budget-report
  Np : string; Msg : string;
  ==========================
  [budget-report Np Msg] : budget-report;)

(datatype pool-allow
  Np : string; Remaining : number;
  ================================
  [pool-allow Np Remaining] : pool-allow;)

(datatype disrupt-command
  Id : string; M : method;
  ========================
  [disrupt-command Id M] : disrupt-command;)

(define cand-id {candidate --> string}
  [candidate Id _ _ _ _ _ _ _ _ _ _ _ _] -> Id)

(define cand-np {candidate --> string}
  [candidate _ Np _ _ _ _ _ _ _ _ _ _ _] -> Np)

(define max0 {number --> number}
  N -> 0 where (< N 0)
  N -> N)

(define keep-eligible {(list candidate) --> (list candidate)}
  [] -> []
  [C | Rest] -> (cons C (keep-eligible Rest)) where (eligible? C)
  [_ | Rest] -> (keep-eligible Rest))

(define keep-method {(list candidate) --> method --> (list candidate)}
  [] _ -> []
  [C | Rest] M -> (cons C (keep-method Rest M)) where (= (classify C) M)
  [_ | Rest] M -> (keep-method Rest M))

(define any-method? {(list candidate) --> method --> boolean}
  [] _ -> false
  [C | Rest] M -> true where (= (classify C) M)
  [_ | Rest] M -> (any-method? Rest M))

\* active-method: the single highest-priority method with an eligible candidate,
   mirroring the Go controller running exactly one method per reconcile. *\
(define active-method {(list candidate) --> method}
  Cs -> m-emptiness where (any-method? Cs m-emptiness)
  Cs -> m-staticdrift where (any-method? Cs m-staticdrift)
  Cs -> m-drift where (any-method? Cs m-drift)
  _ -> m-none)

(define allow-for {string --> (list pool-allow) --> number}
  _ [] -> 0
  Np [[pool-allow Np2 R] | _] -> R where (= Np Np2)
  Np [_ | Rest] -> (allow-for Np Rest))

(define dec-allow {string --> (list pool-allow) --> (list pool-allow)}
  _ [] -> []
  Np [[pool-allow Np2 R] | Rest] -> (cons [pool-allow Np2 (- R 1)] Rest) where (= Np Np2)
  Np [E | Rest] -> (cons E (dec-allow Np Rest)))

(define resolve-one
  {string --> number --> budget-eval --> ((list pool-allow) * (list budget-report))
   --> ((list pool-allow) * (list budget-report))}
  Np Disr [allowed N] RB -> (@p (cons [pool-allow Np (max0 (- N Disr))] (fst RB)) (snd RB))
  Np Disr [budget-error M] RB -> (@p (cons [pool-allow Np 0] (fst RB)) (cons [budget-report Np M] (snd RB))))

\* resolve-budgets: per-pool remaining allowance = allowed - disrupting, clamped
   at 0. A budget parse error fails CLOSED (remaining 0) AND is carried out as a
   budget-report, unlike Go's MustGetAllowedDisruptions which returns 0 silently. *\
(define resolve-budgets
  {(list pool-budget) --> ((list pool-allow) * (list budget-report))}
  [] -> (@p [] [])
  [[pool-budget Np Nodes Num Disr] | Rest]
    -> (resolve-one Np Disr (budget-allowed Nodes Num) (resolve-budgets Rest)))

(define select-h
  {(list candidate) --> method --> (list pool-allow) --> (list disrupt-command)
   --> (list disrupt-command)}
  [] _ _ Acc -> (reverse Acc)
  [C | Rest] M Allow Acc
    -> (if (> (allow-for (cand-np C) Allow) 0)
           (select-h Rest M (dec-allow (cand-np C) Allow)
                     (cons [disrupt-command (cand-id C) M] Acc))
           (select-h Rest M Allow Acc)))

\* plan: the disruption decision. Gates candidates, picks the one active method
   by priority, and emits budget-limited commands (decision-id + method) that the
   shell seeds into the command ledger as pending entries. Second component
   carries any per-pool budget errors for the shell to log/event. *\
(define plan
  {(list candidate) --> (list pool-budget)
   --> ((list disrupt-command) * (list budget-report))}
  Cands Budgets
    -> (let Elig (keep-eligible Cands)
       (let M (active-method Elig)
       (let RB (resolve-budgets Budgets)
         (if (= M m-none)
             (@p [] (snd RB))
             (@p (select-h (keep-method Elig M) M (fst RB) []) (snd RB)))))))

(define plan-commands
  {(list candidate) --> (list pool-budget) --> (list disrupt-command)}
  Cs Bs -> (fst (plan Cs Bs)))

(define plan-errors
  {(list candidate) --> (list pool-budget) --> (list budget-report)}
  Cs Bs -> (snd (plan Cs Bs)))
