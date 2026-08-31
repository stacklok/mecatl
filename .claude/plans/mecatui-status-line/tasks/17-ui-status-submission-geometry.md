---
id: 17-ui-status-submission-geometry
title: Submit status input outside rendering with stable lanes
blocked_by: [16-statusline-api-cleanup]
status: done
branch: plan-mecatui-status-line/17-ui-status-submission-geometry
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-status-line
---

# Task brief

Make View/render functions side-effect free. Build one pure status geometry snapshot with stable reserved header/footer status lanes. Submit Input after complete reducer transitions, resize, and relevant state changes. Distinguish thinking from running_tool and publish tool progress activity. Add tests proving normal state changes preserve submitted budgets and source submission never occurs in render paths.
