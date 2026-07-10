\* Pure Shen functions for the shencore Phase 0 smoke test.

These carry no karpenter decision logic; they exercise the Go<->Shen boundary:
value round-tripping, a raised Shen condition, and a non-terminating function
for the step-budget path. *\

\* list-sum: fold a list of integers to their sum. *\
(define list-sum
  [] -> 0
  [X | Xs] -> (+ X (list-sum Xs)))

\* echo: return the argument unchanged, to prove structural round-tripping of
   arbitrary sexpr values (nested lists, symbols, strings). *\
(define echo
  X -> X)

\* boom: always raise a Shen condition, to prove error mapping to *ShenError. *\
(define boom
  _ -> (error "kaboom"))

\* spin: never terminates, to prove the cooperative step budget trips. *\
(define spin
  X -> (spin X))
