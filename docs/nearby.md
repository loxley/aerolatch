# Nearby aircraft

`aerolatch nearby` searches around a fixed reference point. It does not detect or
follow your device's position. Radius and distances are **nautical miles**; one
nautical mile is 1.852 km. Bearings are from the reference point clockwise from
true north, independently of the aircraft's own ground track.

## Choose the reference point

Supply exactly one of:

```sh
# City or postal code, with an optional ISO two-letter country filter.
./aerolatch nearby --city Stockholm --country SE --tui

# A specific place ID from a previous ambiguous search.
./aerolatch nearby --place-id 2673730 --tui

# Precise coordinates. Both flags are required; zero is valid.
./aerolatch nearby --lat 59.33 --lon 18.07 --tui
```

City searches use Open-Meteo's `/v1/search`, and place IDs use `/v1/get`. Results
include region, country, and coordinates. Up to ten candidates are requested;
exact city-name matches are preferred over prefix suggestions. If more than one
matches, interactive non-JSON commands open a location picker.
Use arrows or `j`/`k`, then Enter; Esc, `q`, or Ctrl+C leaves the picker.
Piped output and JSON commands never prompt: an ambiguity error lists candidate
`--place-id` values and no aircraft requests are made. Try a more specific city
query or `--country` to narrow a search.

The geocoder is called once per invocation, not on every aircraft refresh or when
returning from the detail screen. No persistent location file is written.
Coordinates bypass geocoding. City coordinates are representative city points,
not the user's exact position or necessarily the geographical centre.

Location data: [Open-Meteo](https://open-meteo.com/en/docs/geocoding-api) /
[GeoNames](https://www.geonames.org). The free Open-Meteo service needs no API key
for non-commercial use; its terms and attribution requirements apply.

## Search and output options

| Flag | Default | Meaning |
| --- | --- | --- |
| `--radius` | `25` | Integer radius, 1–250 nautical miles |
| `--interval` | `10s` | Delay after a response, 5 seconds to 24 hours |
| `--timeout` | `15s` | Per-request timeout, greater than zero and at most 5 minutes |
| `--provider` | `adsb.lol` | `adsb.lol` or `adsb.fi` |
| `--swedavia` | `auto` | Optional selected-aircraft airport enrichment; `auto` or `off` |
| `--tui` | off | Interactive nearby list and detailed tracking |
| `--once` | off | One area response and exit |
| `--json` | off | Newline-delimited JSON snapshots |

`--tui` requires terminal input and output and cannot be combined with `--once`
or `--json`. For example:

```sh
./aerolatch nearby --city Stockholm --country SE --radius 50 --once
./aerolatch nearby --lat 59.33 --lon 18.07 --provider adsb.fi --once --json
```

The area APIs are:

- ADSB.lol: `/v2/point/{lat}/{lon}/{radius}`.
- ADSB.fi: `/v3/lat/{lat}/lon/{lon}/dist/{radius}`. Its deprecated v2 area endpoint
  is not used. ADSB.fi's personal/non-commercial terms and attribution apply;
  see [its API documentation](https://github.com/adsbfi/opendata).

Each poll is one area request, not one request per aircraft. Timeouts, transient
errors, and HTTP 408/429/5xx use exponential retry backoff, respecting Retry-After.
Other HTTP client errors stop polling. `--once` does not retry. ADSB.fi requests
are spaced by at least one second across list/detail transitions, with server
Retry-After cooldowns preserved. View changes cancel and join the previous poller.

## Dashboard navigation

- **↑↓ / j k:** select an aircraft; **Page Up/Down**, **Home/End:** navigate rows.
- **Enter:** open the existing detailed radar dashboard using the selected hex.
- **Esc in detail:** return to the nearby list; if help is open, close it first.
- **? / Esc:** open/close nearby help.
- **q / Ctrl+C:** quit from either screen and restore the terminal.

Selection follows the hex, not the row number, as distance sorting changes. When
the selected aircraft disappears or expires, Enter cannot silently follow another
aircraft; use an arrow key to choose again. Returning from detail keeps the prior
selection if it is still present in the new area response.

Detail is physical-aircraft tracking by hex. The selected observation appears
immediately as **LAST OBSERVATION / NOT CURRENT**, with its original timestamps,
until the first detail response arrives. A blank broadcast callsign stays blank;
the header separately retains a clearly labelled **last-known callsign**.

When a callsign is known, an independent background lookup tries to resolve its
airline and origin/destination using adsbdb. Successes are cached for 30 minutes
and failures for five minutes within that inspection, with a bounded cache.
Changing callsign clears/refetches the reference and cancels any superseded
lookup; late replies cannot replace the new aircraft/callsign context. When the
current callsign is absent or observations are historical, an available route is
labelled **Last-known reference**. Unknown references remain unavailable without
stopping telemetry or keyboard input.

These are reference mappings, not confirmed dated services or airport reports.
A resolved destination is supplied to the **same tracker and estimators used by
normal flight tracking**, with `adsbdb` reference provenance. Once usable airport
coordinates and telemetry are available, distance, phase, rough touchdown, and
arrival evidence populate just as they do with `track`. A reference arriving
after the first live response re-evaluates that fix without another API request,
changing its timestamps, or manufacturing additional surface/approach evidence.
While the destination is unresolved, a bounded history of actual live reports
(up to 128 reports / ten minutes) preserves observations for the initial binding.
Those reports are replayed through the shared estimators at their original times,
so a delayed route response does not lose an observed approach or landing. This
pending history is cleared across nonblank callsign changes and is not carried
from one bound destination into another.

The hex lock and measurement history survive route updates. Destination changes
and callsign-reference invalidation reset destination-dependent ETA/arrival
evidence and ground dwell, preserving the physical aircraft identity and an
already-triggered next-leg guard. Blank callsigns retain the last matching
reference; a different nonblank broadcast immediately prevents the old reference
from being used, even while the replacement lookup is pending.

Touchdown can still be withheld for the same reasons as normal tracking, such as
stale telemetry, movement away from the destination, or missing measurements.
An airborne report is shown as **AIRBORNE**, with **Surface: not observed** until
there is surface evidence. To choose a destination explicitly instead, use:

```sh
./aerolatch track --hex 4aca87 --destination ARN --tui
```

The same terminal session stays open while changing views. Only the active view
polls, so returning to nearby starts a fresh area query. Detailed tracking history
is scoped to that inspection; closing it does not persist a flight session.

During requests the footer shows **Fetching aircraft** or a **Waiting for provider**
countdown. Reference resolution has its own status in the header/help. Esc/q/Ctrl+C
cancel work without waiting for HTTP timeouts or cooldowns. Rendering uses a
shared palette and never reads terminal input for colour detection: `COLORFGBG`
provides a light/dark hint when available, with dark as the fallback.

## Swedavia airport information

When `SWEDAVIA_API_KEY` is exported and `--swedavia auto` is used, selecting an
aircraft enables the same Swedavia watcher/matching rules as normal tracking.
The resolved route discovers a covered destination's arrivals board, or a covered
origin's departures board. For example, **SFT → ARN** selects Arlanda arrivals;
**CPH → AMS** has no Swedavia coverage. No airport requests are made for the area
list or for uncovered routes.

```sh
./aerolatch nearby --city Stockholm --country SE --tui
./aerolatch nearby --city Stockholm --country SE --swedavia off --tui
```

The background worker matches and pins a dated leg using operating/codeshare
flight numbers, callsign, and available aircraft registration, while tracking
continues by hex. Current callsign/registration conflicts prevent an unrelated
record from being applied. Missing, ambiguous, failed, or stale airport data is
labelled accordingly. A covered route with no key shows **Swedavia: no key**;
other waiting/coverage/opt-out states are visible in the footer/help.

Airport I/O is independent of live-position polling and its retry waits. Checks
continue using the retained aircraft identity when ADS-B is no longer visible.
The existing five-/15-/30-/60-minute watcher refresh rules and API backoff apply;
nearby polling does not fetch a board every ten seconds. One credential-scoped
client/cache is shared across all selections in the browser session. Leaving
detail cancels and joins airport work; cancelled requests are not cached as
failures for later selections. A changed route/flight context discards the old
airport selection and ignores cancelled responses.

The arrival panel shows **Airport: LANDED (Swedavia)** and actual/estimated times
separately from **Inferred: POSSIBLE LANDING/LIKELY LANDED/UNKNOWN**. An airport
arrival never fabricates a position, a surface fix, or corroborating ADS-B evidence.
A fresh matched airport route supplies dated destination provenance to the shared
tracker, and reported terminal/diversion states use its existing inference rules.

See [Swedavia setup, coverage, dates, matching, and quotas](swedavia.md).

## Freshness and distance

Only valid six-digit ICAO hex addresses with valid coordinates and position times
no more than **90 seconds old** or **5 seconds ahead** are included. When a contact
time exists, it must meet the same age limits. Unknown measurement values stay
unavailable, not zero. Surface reports show **GROUND**; they do not establish a
landing. Non-ICAO addresses are excluded because detail navigation uses ICAO hex.

The freshest usable fix per hex is selected, then the radius is checked locally.
Rows are sorted by great-circle distance, with hex as a deterministic tie-break.
Fixes expire from the TUI between polls as the clock advances; screen ticks,
scrolling, and help do not make network requests.

On errors, previous rows can be shown under **NOT CURRENT** with their original
fix times. These rows are not selectable. Successful empty results clear the
previous list. No results means no fresh valid positions in the feed and radius,
not proof that there are no aircraft nearby. Distance is not slant range or a
prediction of visibility; clouds, terrain, altitude, and receiver coverage matter.

## JSON fields

Each snapshot includes:

- `checked_at`, `source`, `status` (`visible`, `empty`, `retrying`, or `error`).
- `location`: name, optional place ID/region/country, latitude, longitude,
  `source` (`city` or `coordinates`), and optional `data_source`.
- `radius_nm`, `aircraft`, and `excluded` (unusable, out-of-radius, or duplicate
  records excluded from this response).
- Each aircraft's existing reported fields, plus `distance_nm`, `bearing_deg`,
  and the eight-point compass `direction`. `bearing_deg` is observer-relative;
  `ground_track_deg` is the aircraft's direction of movement.
- On failure, optional `error`, `retry_at`, `last_observed`, and `last_seen_at`.
  Retained records are separate from the empty current `aircraft` array.

Detailed inspection snapshots additionally separate `inspection` metadata
(last-known callsign/observation time, reference status/callsign/error, and request
progress) from raw aircraft fields. Metadata updates do not refresh the live
`checked_at` or aircraft position timestamps.
`inspection.swedavia_status` identifies configuration/discovery state or the
current report match. The existing `airport_report` schema carries airport data.

No location prompts, progress messages, or attribution banners are written into
JSON output. Location provenance and live-provider attribution are carried in the
data, with human-readable notices in plain/TUI output and the documentation.
