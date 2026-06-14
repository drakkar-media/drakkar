# TRaSH Guides Sync

Drakkar supports importing custom formats and release block rules from [TRaSH Guides](https://trash-guides.info/). Import is manual — there is no automatic sync or scheduled pull. You paste the JSON from the TRaSH Guides site into the import dialog.

## What gets imported

- **Custom formats** — regex-based scoring rules with a score value. After import they appear in Settings > Quality > Custom Formats with the same name as on the TRaSH site.
- **Release block rules** — release group blocks and title pattern rules. After import they appear in Settings > Quality > Release Rules with `source: trash`.

## Import steps

### Custom formats

1. Open [trash-guides.info](https://trash-guides.info/) and navigate to the Radarr or Sonarr custom format you want.
2. Copy the JSON payload (the `json` block on the TRaSH page).
3. In Drakkar, go to **Settings > Quality > Custom Formats**.
4. Click **Import from TRaSH**.
5. Paste the JSON and confirm.

The format is created if it does not exist. If a format with the same name already exists, it is updated.

### Release block rules

1. Find the relevant blocked-release-groups or release-filtering JSON on trash-guides.info.
2. In Drakkar, go to **Settings > Quality > Release Rules**.
3. Click **Import**.
4. Paste the JSON array of rule objects.

The import endpoint (`POST /api/release-block-rules/import`) validates all rules before writing any. If any rule fails validation the entire import is rejected.

## Source tracking

Imported custom formats do not track their source — they look identical to locally-created ones and can be freely edited or deleted.

Release block rules track their origin in the `source` column:

- `custom` — created locally. Can be fully edited and deleted.
- `trash` — imported from TRaSH Guides. Only `enabled` and `note` can be changed. The `rule_type`, `pattern`, `media_type`, `action`, and `score_penalty` fields are locked to prevent silent divergence from the upstream definition.

To fully replace a trash rule, delete it and re-import the updated version.

## Keeping rules up to date

TRaSH Guides releases are updated frequently. Drakkar does not auto-sync. Suggested workflow:

1. Subscribe to the TRaSH Guides changelog RSS or Discord.
2. When a format you care about changes, re-import the updated JSON.
3. For trash-sourced block rules, the import uses upsert logic — re-importing overwrites the locked fields with the new values.

## Local edits and TRaSH rules

If you need to customise a TRaSH-imported release block rule beyond just toggling it on/off:

1. Note the rule's pattern and settings.
2. Delete the trash rule.
3. Create a new custom rule with your desired settings.

Custom rules can be fully edited at any time. They will not be overwritten by a future TRaSH import unless you explicitly import and overwrite them.

## Checking import results

After importing release block rules, the API returns:

```json
{
  "imported": 15,
  "total": 15
}
```

If `imported` is less than `total`, some rules failed silently (usually a duplicate key conflict). Check the application logs for details.

## API reference

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/release-block-rules/import` | Bulk import release block rules |
| `POST` | `/api/custom-formats` | Create a single custom format |
| `PUT` | `/api/custom-formats/{id}` | Update a custom format |

There is no dedicated bulk-import endpoint for custom formats — import them one at a time via `POST /api/custom-formats` or write a short script that iterates the TRaSH JSON array.
