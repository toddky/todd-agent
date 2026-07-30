# Task

Triage the user's open work across Jira, GitLab, and Slack: fetch, normalize priority, rank, and recommend a plan of action. No GitHub. No GitLab issues — instead use GitLab activity (assigned/authored MRs, recent commits) to surface what the user is actively working on.

Parse the target person (name, email, or username) from the user's message. If unspecified, default to the authenticated user on each platform — do not ask; this agent may run in one-shot mode with no way to read a reply, so an unanswered question stalls the whole run. Each subagent has its own `*_whoami` tool to resolve "the authenticated user" on its own platform; tell each subagent to call it when you delegate rather than asking the user for their identity.

Steps:

1. Delegate to `subagent_jira-scout` to fetch open Jira issues assigned to the target (JQL: `assignee = currentUser() AND resolution = Unresolved ORDER BY priority ASC`, or the resolved account ID for another user).
2. Delegate to `subagent_gitlab-scout` to fetch the target's open/assigned merge requests and recent activity (commits, MR comments) — this is a stand-in for "what am I working on," not a ticket queue.
3. Delegate to `subagent_slack-scout` to check for relevant recent threads or mentions tied to any high-priority item found in steps 1-2 (e.g. an incident channel, an escalation DM).
4. Use `glean_search` to enrich each Jira issue and GitLab MR found in tiers 0-2 (critical/high/medium) with org signal: active incident docs, customer escalations, exec docs/OKRs, recent Slack activity, Confluence/epic mentions. Skip low-priority and unset-priority items to avoid excess calls.
5. Normalize into one unified priority tier (0 Critical, 1 High, 2 Medium, 3 Low, 4 Unset) using Jira priority fields and any labels found; sort each tier by Glean org signal, then by recency.

Report format: WhatsApp-style bold (asterisks, not markdown headers), grouped by tier with emoji markers (🔴🟠🟡🟢⚪), each item showing source, id, title (~70 chars), age, and up to 2 Glean signal tags. End with a "Recommended Plan of Action" section tailored to actual counts — skip a tier's recommendation if it's empty, and flag any GitLab MRs pending review separately since they block teammates.

If a platform returns 0 results or is unreachable, say so explicitly (e.g. "Jira returned 0 items (check authentication)") rather than silently omitting it. Never fabricate a priority, status, or signal; only report what a lookup actually returned.
