# Renewal fixture correction

PIN run37804916619 and continuity run37805688993 failed only the standing-renewal lock-wait test. An earlier translation entitlement fixture changed the SQL ended_at column while leaving document.ended_at48hours in the future. Maintenance selects by SQL due time, then rechecks the serialized subscription and stops when it is not yet due. That deliberately inconsistent older fixture can prevent reaching the later PIN account lock.

This correction changes only that synthetic expiry write to update both representations atomically at the same existing fixture timestamp. Renewal assertions and runtime code are unchanged. Integration race compilation passes locally; actual418-case/55-restart rerun remains required.

Separate runtime robustness follow-up: an inconsistent projection should produce bounded per-row error/quarantine or cursor progress so unrelated due renewals cannot starve indefinitely. It must never silently settle or renew corrupt financial state. That change is deliberately outside this fixture correction and awaits a green baseline.
