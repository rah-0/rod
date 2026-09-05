# Correct the documentation links for element wait states

Priority: P3.

Source: upstream [#1182](https://github.com/go-rod/rod/pull/1182).

## Current evidence

The comments immediately above `Element.WaitEnabled` and `Element.WaitWritable` in [element.go](../../element.go) still have their attribute references reversed. `WaitEnabled` links to `readonly`, while `WaitWritable` links to `disabled`. A reader following the API documentation is therefore directed to a different HTML state from the one named by the method.

These attributes have different behavior and are not interchangeable. The PR's narrowly scoped comment correction still applies to this fork and requires no API or runtime changes.

## Change

Point `WaitEnabled` to the [disabled attribute reference](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Attributes/disabled) and label it accordingly. Point `WaitWritable` to the [readonly attribute reference](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Attributes/readonly) and label it accordingly.

Keep the task limited to the two incorrect comments. This documentation correction does not establish that the runtime implementations cover every HTML disabled or readonly case, and it should not be described as a behavioral fix.

## Acceptance

- Each method's comment names and links to its corresponding attribute.
- Go documentation displays the corrected references.
- The diff contains only the intended comment changes and passes `git diff --check`.
- No new behavioral test is required for this comment-only task.
