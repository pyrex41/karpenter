\* ===================================================================
   command.shen — the disruption command ledger state machine.

   This is the decision-side of karpenter's disruption command lifecycle
   (today implicit across pkg/controllers/disruption/queue.go: queue-map
   membership + Replacement.Initialized + Command.Succeeded + the
   recoverable/unrecoverable error class). Here it is an EXPLICIT value:
   a closed sequent-calculus datatype whose transition function is total
   and type-checked at load time under (tc +).

   Why this kills the K1 wedge: in the Go code an unexpected event in an
   unexpected state is an unhandled path that can leave a command stuck
   (tainted-but-never-progressing). Here every (state, event) pair has a
   defined transition — an unexpected event is an explicit self-loop, and
   a timeout is a transition the machine cannot forget — so "wedged
   command" stops being a reachable state. The Go table-driven tests
   assert the full state x event grid is total (no partial-function
   fault) and that terminal states absorb.

   Vocabulary mirrors the Go shell so the executor maps 1:1:
     apply-taint          <- markDisrupted / DisruptedNoScheduleTaint
     launch-replacements  <- createReplacementNodeClaims
     delete-candidates    <- Delete(candidate NodeClaim)
     remove-taint         <- RequireNoScheduleTaint(false)  (rollback)
     clear-condition      <- ClearNodeClaimsCondition(DisruptionReason)
   Timeout mirrors queue.go:189-195 — clock.Since(CreationTimestamp) >
   retryDuration promotes a recoverable wait to an unrecoverable abort.
   The (tc +) gate is applied EXTERNALLY (make shen-check / hack/shen-check.sh
   evaluates (tc +) before loading), because Shen's loader samples the
   typecheck flag once at load start — an in-file (tc +) would be too late to
   check this file's own defines. Every define below therefore carries a
   {..} signature so the whole file type-checks as one batch under the gate.
   =================================================================== *\

\* The command lifecycle states (queue.go implicit phases made explicit). *\
(datatype command-state
  ________________ pending : command-state;
  ________________ tainted : command-state;
  ________________ launching : command-state;
  ________________ awaiting-ready : command-state;
  ________________ deleting : command-state;
  ________________ done : command-state;
  ________________ rolled-back : command-state;)

\* Outcomes the shell reports back each tick (the (events ...) contract),
   plus `tick`: a clock advance carrying no external outcome, which drives
   pending kick-off and timeout evaluation. *\
(datatype command-event
  ________________ tick : command-event;
  ________________ taint-ok : command-event;
  ________________ taint-failed : command-event;
  ________________ launched : command-event;
  ________________ launch-failed : command-event;
  ________________ registered : command-event;
  ________________ deleted : command-event;
  ________________ delete-failed : command-event;)

\* Imperative actions the shell executor performs (idempotent by decision-id). *\
(datatype command-action
  ________________ apply-taint : command-action;
  ________________ launch-replacements : command-action;
  ________________ delete-candidates : command-action;
  ________________ remove-taint : command-action;
  ________________ clear-condition : command-action;)

\* timed-out: clock.Since(created) > retry. *\
(define timed-out
  {number --> number --> number --> boolean}
  Clock Created Retry -> (> (- Clock Created) Retry))

\* command-step: the total transition. Returns (actions * state'). Every
   non-terminal state defines the happy-path advance, its failure/timeout
   rollback, and a catch-all self-loop for out-of-order/stale events;
   terminal states absorb. Rollback emits remove-taint + clear-condition,
   mirroring the queue.go unrecoverable-error cleanup. *\
(define command-step
  {command-state --> command-event --> number --> number --> number
   --> ((list command-action) * command-state)}

  pending tick _ _ _ -> (@p [apply-taint] tainted)
  pending _ _ _ _ -> (@p [] pending)

  tainted taint-ok _ _ _ -> (@p [launch-replacements] launching)
  tainted taint-failed _ _ _ -> (@p [remove-taint clear-condition] rolled-back)
  tainted tick Clock Created Retry -> (@p [remove-taint clear-condition] rolled-back)
    where (timed-out Clock Created Retry)
  tainted _ _ _ _ -> (@p [] tainted)

  launching launched _ _ _ -> (@p [] awaiting-ready)
  launching launch-failed _ _ _ -> (@p [remove-taint clear-condition] rolled-back)
  launching tick Clock Created Retry -> (@p [remove-taint clear-condition] rolled-back)
    where (timed-out Clock Created Retry)
  launching _ _ _ _ -> (@p [] launching)

  awaiting-ready registered _ _ _ -> (@p [delete-candidates] deleting)
  awaiting-ready launch-failed _ _ _ -> (@p [remove-taint clear-condition] rolled-back)
  awaiting-ready tick Clock Created Retry -> (@p [remove-taint clear-condition] rolled-back)
    where (timed-out Clock Created Retry)
  awaiting-ready _ _ _ _ -> (@p [] awaiting-ready)

  \* Once replacements are live the command never rolls back: a stuck
     deletion keeps retrying (idempotent), so deleting is never a dead end
     and never un-cordons nodes whose replacements already exist. *\
  deleting deleted _ _ _ -> (@p [] done)
  deleting delete-failed _ _ _ -> (@p [delete-candidates] deleting)
  deleting tick _ _ _ -> (@p [delete-candidates] deleting)
  deleting _ _ _ _ -> (@p [] deleting)

  done _ _ _ _ -> (@p [] done)
  rolled-back _ _ _ _ -> (@p [] rolled-back))

\* ---- Ledger layer -------------------------------------------------------- *\

\* The ledger is the load-bearing novelty: command state round-tripped each
   tick as an explicit value, keyed by generated decision-id (a string, not
   a deletable k8s object). ledger-tick is a pure function of
   (ledger, events, clock) so a tick is fully replayable. *\

(datatype ledger-entry
  Id : string; St : command-state; Created : number;
  ===================================================
  [Id St Created] : ledger-entry;)

(datatype id-event
  Id : string; Ev : command-event;
  ================================
  [Id Ev] : id-event;)

(datatype id-action
  Id : string; Act : command-action;
  ==================================
  [Id Act] : id-action;)

\* events-for: the events addressed to one decision-id, in order. *\
(define events-for
  {string --> (list id-event) --> (list command-event)}
  _ [] -> []
  Id [[Id2 Ev] | Rest] -> (cons Ev (events-for Id Rest)) where (= Id Id2)
  Id [_ | Rest] -> (events-for Id Rest))

\* drive: a quiet entry (no events this tick) still gets one `tick`, so
   pending kicks off and timeouts are evaluated every quiet round. *\
(define drive
  {(list command-event) --> (list command-event)}
  [] -> [tick]
  Evs -> Evs)

\* step-fold: thread one entry's events through command-step, accumulating
   actions in order. *\
(define step-fold
  {command-state --> (list command-event) --> number --> number --> number
   --> (list command-action) --> ((list command-action) * command-state)}
  St [] _ _ _ Acc -> (@p Acc St)
  St [Ev | Rest] Clock Created Retry Acc
    -> (let R (command-step St Ev Clock Created Retry)
         (step-fold (snd R) Rest Clock Created Retry (append Acc (fst R)))))

(define tag-actions
  {string --> (list command-action) --> (list id-action)}
  _ [] -> []
  Id [A | As] -> (cons [Id A] (tag-actions Id As)))

(define ledger-tick-h
  {(list ledger-entry) --> (list id-event) --> number --> number
   --> (list ledger-entry) --> (list id-action)
   --> ((list ledger-entry) * (list id-action))}
  [] _ _ _ AccL AccA -> (@p AccL AccA)
  [[Id St Created] | Rest] Events Clock Retry AccL AccA
    -> (let R (step-fold St (drive (events-for Id Events)) Clock Created Retry [])
         (ledger-tick-h Rest Events Clock Retry
           (append AccL [[Id (snd R) Created]])
           (append AccA (tag-actions Id (fst R))))))

\* ledger-tick: (ledger, events, clock, retry) -> (ledger' * actions). *\
(define ledger-tick
  {(list ledger-entry) --> (list id-event) --> number --> number
   --> ((list ledger-entry) * (list id-action))}
  Ledger Events Clock Retry -> (ledger-tick-h Ledger Events Clock Retry [] []))

\* ledger-gc: drop terminal (done | rolled-back) entries. Checkpoint hygiene
   for the shell; kept separate so ledger-tick stays observable in tests. *\
(define terminal?
  {command-state --> boolean}
  done -> true
  rolled-back -> true
  _ -> false)

(define ledger-gc
  {(list ledger-entry) --> (list ledger-entry)}
  [] -> []
  [[Id St Created] | Rest] -> (ledger-gc Rest) where (terminal? St)
  [E | Rest] -> (cons E (ledger-gc Rest)))

\* ---- Go-facing projections ----------------------------------------------
   The typed core returns Shen products (@p ...); those do not cross the
   sexpr codec, so these thin wrappers project each result into plain
   symbol/list shapes the Go engine can decode. They add no logic. *\

(define step-state
  {command-state --> command-event --> number --> number --> number --> command-state}
  St Ev C Cr R -> (snd (command-step St Ev C Cr R)))

(define step-actions
  {command-state --> command-event --> number --> number --> number --> (list command-action)}
  St Ev C Cr R -> (fst (command-step St Ev C Cr R)))

(define ledger-next
  {(list ledger-entry) --> (list id-event) --> number --> number --> (list ledger-entry)}
  L Evs C R -> (fst (ledger-tick L Evs C R)))

(define ledger-actions
  {(list ledger-entry) --> (list id-event) --> number --> number --> (list id-action)}
  L Evs C R -> (snd (ledger-tick L Evs C R)))
