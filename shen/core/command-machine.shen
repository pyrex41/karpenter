\* ===================================================================
   command-machine.shen — the pure disruption ledger tick.

   Builds on the command lifecycle types + transition in
   shen/types/command.shen (load that first; this file references its
   datatypes and command-step). Here is the load-bearing novelty: the
   ledger — command state round-tripped each tick as an explicit value,
   keyed by a generated decision-id (a string, NOT a deletable k8s
   object), so a checkpointed ledger reconciles by id and the K1 wedge
   (a command whose k8s object vanished, stranding its state) cannot
   occur. ledger-tick is a pure function of (ledger, events, clock,
   retry), so an entire disruption poll is replayable.

   Contract shape (see the plan's Data contract section):
     (disrupt (clock) (snapshot) (ledger)) ->
       (disrupt-result (ledger') (actions ... (for-decision <id>)))
   ledger-tick computes the (ledger', actions) core of that result; the
   shell wraps snapshot decoding and action execution around it.

   Type-checked as one batch under the external (tc +) gate; every define
   carries a {..} signature. See shen/types/command.shen for why the gate
   is external.
   =================================================================== *\

\* A ledger entry: decision-id, its command state, and the clock reading at
   which the command was created (for timeout arithmetic). *\
(datatype ledger-entry
  Id : string; St : command-state; Created : number;
  ===================================================
  [Id St Created] : ledger-entry;)

\* An event addressed to a specific decision-id (the shell's outcome report). *\
(datatype id-event
  Id : string; Ev : command-event;
  ================================
  [Id Ev] : id-event;)

\* An action tagged with the decision-id it belongs to (for-decision <id>). *\
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

\* ---- Go-facing projections (ledger-tick returns a product @p ...). ------- *\

(define ledger-next
  {(list ledger-entry) --> (list id-event) --> number --> number --> (list ledger-entry)}
  L Evs C R -> (fst (ledger-tick L Evs C R)))

(define ledger-actions
  {(list ledger-entry) --> (list id-event) --> number --> number --> (list id-action)}
  L Evs C R -> (snd (ledger-tick L Evs C R)))
