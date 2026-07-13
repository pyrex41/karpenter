\* Stub Shen disruption decision core.

   Implements the shencore.disrupt entrypoint of the disruption decision
   contract: it takes the schema (input ...) snapshot and returns an (output ...)
   decision. This stub always decides to do nothing — (output (commands)) — so the
   shadow/shen decision-engine plumbing can be exercised end-to-end before the real
   decision core (shen/core/disruption.shen) lands. Replace by loading the real
   source via SHENCORE_DISRUPTION_SOURCE. *\

(define shencore.disrupt
  _ -> [output [commands]])
