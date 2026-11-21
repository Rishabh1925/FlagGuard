# FlagGuard voiceover

Play [the silent walkthrough](flagguard-demo-silent.mp4) from the beginning. Each row is one take. Read the last column aloud and leave any spare second for the viewer to read the screen. Three waits are marked **4×** in the video. The opening replay and the live workspace show two different incidents.

| Time | On screen | Narration |
| --- | --- | --- |
| 0:00–0:07 | Landing page | What if the checkout you're testing starts failing? I built FlagGuard to switch it off automatically. |
| 0:07–0:13 | Compare readings | In this saved incident, almost twenty-eight percent of experimental checkouts failed. The original checkout stayed healthy. |
| 0:13–0:18 | Inspect the switch | FlagGuard disabled the flag and its experiment override, then checked both in Flagsmith. |
| 0:18–0:24 | Verify checkout | Did checkout actually switch back? All twenty follow-up test requests succeeded on the original version. |
| 0:24–0:34 | Policy bench | What if we'd allowed more errors? I'll raise that limit. The slowest checkouts still cross the latency limit. Let's restore the original policy. |
| 0:34–0:39 | Try your own rollout | One command starts the self-hosted lab. Let's see it run. |
| 0:39–0:53 | Workspace and beginner guide | Flagsmith assigns synthetic traffic to two checkouts. Control is the original. Variant B is the one we're testing. Prometheus measures errors and response times. Right now, both versions are healthy. |
| 0:53–1:04 | Inject a fault | I'll inject a fault into Variant B. Watch errors and P ninety-nine latency. That's the time within which ninety-nine percent of requests finish. |
| 1:04–1:16 | Breach confirmation | The lines have crossed their limits. Before FlagGuard acts, it needs enough traffic and a breach that lasts fifteen seconds. A single slow request wouldn't trip this policy. |
| 1:16–1:24 | Confirmation continues | The original checkout is still responding normally. Now watch the guardrail status. We're waiting for Flagsmith to confirm the change. |
| 1:24–1:31 | Rollback verified | There's the rollback. FlagGuard read both settings back from Flagsmith. The rollout stays locked until someone reviews it. |
| 1:31–1:40 | Incident history and decision inspector | Let's open that exact incident. These readings stay with the decision. Both the flag and the experiment rollout went from on to off. |
| 1:40–1:49 | Recorded measurements | Variant B hit twenty-six percent errors against a five percent limit. Control had zero observed errors. That sustained error rate triggered this rollback. |
| 1:49–1:55 | Export report and return to Overview | I can export the evidence for my team. Back in Overview, I'll review recovery. |
| 1:55–2:11 | Confirm re-enabling | The rollback worked. I also checked twenty fresh requests during this run. All twenty succeeded on control. Here I confirm recovery, which stops the demo fault and re-enables the rollout. |
| 2:11–2:28 | Wait for fresh traffic | The dashboard briefly marks the readings stale while it waits for fresh traffic. Teams can use their own Prometheus queries and try shadow mode first. That records what the policy would have done before you let it change a flag. |
| 2:28–2:36 | Healthy overview | Checkout is healthy again. The incident still shows what failed, why FlagGuard acted, and exactly what changed in Flagsmith. |
