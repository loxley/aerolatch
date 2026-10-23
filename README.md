# Aerolatch

A small Go CLI for tracking a live flight by passenger flight number, callsign,
aircraft hex address, or registration. Uses
free public APIs; live tracking requires no API key. Optional Swedavia airport
schedules and reported status use a free subscription key. This is an independent
project, not an official Flightradar24 client.

## Build

Requires Go 1.22 or newer. The dashboard uses Go 1.22-compatible versions of
Bubble Tea and Lip Gloss; Go downloads the pinned dependencies on the first build.

```sh
go build -o aerolatch .
./aerolatch --help
```

Alternatively, run directly with `go run . track D85373` or install with
`go install .` (installs `aerolatch`; ensure your Go binary directory is on `PATH`).

## Usage

```sh
# Resolve the flight number and poll every 30 seconds; Ctrl+C stops tracking.
./aerolatch track D85373

# Print one snapshot and exit.
./aerolatch track D85373 --once

# Poll every minute.
./aerolatch track D85373 --interval 60s

# Machine-readable output: one JSON object per poll, including errors/gaps.
./aerolatch track D85373 --once --json
./aerolatch track D85373 --json

# Skip the flight-number lookup when the operational callsign is known.
./aerolatch track --callsign NSZ5373

# Follow a specific aircraft directly by ICAO hex address or registration.
./aerolatch track --hex 4aca87 --provider adsb.fi
./aerolatch track --registration SE-RTG --provider adsb.lol --once

# Direct aircraft tracking with destination-dependent estimates and dashboard.
./aerolatch track --hex 4aca87 --destination ARN --provider adsb.fi --tui

# Set the timeout for each API request.
./aerolatch track D85373 --timeout 20s

# Correct an outdated reference destination using an IATA or ICAO airport code.
./aerolatch track RYR9UJ --destination ALC
./aerolatch track --callsign RYR9UJ --destination LEAL

# Select the live-position provider (ADSB.lol is the default).
./aerolatch track RYR9UJ --destination ALC --provider adsb.lol
./aerolatch track RYR9UJ --destination ALC --provider adsb.fi
./aerolatch track --callsign RYR9UJ --provider adsb.fi --once --json

# Full-screen radar-style terminal dashboard.
./aerolatch track NSZ4366 --provider adsb.fi --tui
./aerolatch track --callsign RYR9UJ --destination ALC --provider adsb.fi --tui

# Optional Swedavia airport boards and dated tracking (SWEDAVIA_API_KEY).
./aerolatch departures ARN --date 2026-10-21
./aerolatch arrivals ARN --date 2026-10-21 --json
./aerolatch track D84487 --origin ARN --date 2026-10-21 --tui
```

Flight numbers are case-insensitive; whitespace is ignored (quote input such as
`"d8 5373"`). Flags may precede the flight number or follow it. Refresh intervals
range from 5 seconds to 24 hours; request timeouts must be positive and at most
5 minutes. Polls are sequential, with the interval measured after each response.

For example, adsbdb currently resolves `D85373` to `NSZ5373` with the reference
route Copenhagen Kastrup (`CPH`) → Alicante (`ALC`). That mapping does not imply
the flight is currently airborne.

### Swedavia schedules and reported airport status

When `SWEDAVIA_API_KEY` is configured, tracking automatically enriches flights
touching a Swedavia airport using dated flight records, operational callsigns,
aircraft assignments, reported statuses and times. Use `--origin`/`--destination`
to supply airport hints, or let the reference route discover coverage. Without a
key, live tracking continues normally. `--swedavia off` opts out.

`departures` and `arrivals` list a Swedish airport's local-calendar-day board;
`--date` on `track` selects a specific dated service and waits before acquiring its
aircraft. Airport-reported **LANDED/CANCELLED** is displayed separately from ADS-B
positions and inferred arrival. Deleted records are not treated as cancellations.

See [Swedavia setup, matching, dates, quotas and JSON fields](docs/swedavia.md).

### Aircraft identity and lookup modes

Supply one passenger flight number, or exactly one of these direct selectors:

| Selector | Lookup key | Example |
| --- | --- | --- |
| `--callsign` | Operational callsign, 2–8 letters/digits | `NSZ4366` |
| `--hex` | Six-digit ICAO transponder address, case-insensitive | `4aca87` |
| `--registration` | Aircraft registration, 2–12 letters/digits/internal hyphens | `SE-RTG` |

Direct selectors skip adsbdb flight/route resolution. Add `--destination` to supply
the airport for ETA and arrival inference. Hex addresses normalize to lowercase;
registrations normalize to uppercase while retaining hyphens.

For flight-number, callsign, and registration searches, the tracker **locks onto
the hex address** after receiving a unique match with a valid six-digit address
and a recent contact timestamp (or position timestamp when contact is missing).
Recent means at most 90 seconds old and no more than five seconds ahead of the
local clock. Ambiguous, stale, and non-ICAO-address observations do not acquire
a lock. Searches continue with the original key until acquisition.

Subsequent polls use the hex endpoint, even if the broadcast callsign disappears
or changes. This may preserve surface observations that a callsign query would
miss. During gaps and request failures the lock is retained; the tracker does not
switch back to the original callsign and accidentally acquire a different aircraft
using that callsign. A `--hex` invocation binds directly to the specified address.
Each poll still makes a single live-data request; `--once` does not issue an extra
hex request after initial acquisition.

Plain output distinguishes the **lookup key**, **locked hex**, and **broadcast
callsign**. A missing broadcast callsign stays unavailable rather than being
replaced with the original flight identifier. The dashboard shows the locked hex
and callsign-change events; ETA and trend histories follow the same aircraft
across callsign changes. The original reference route remains reference data.

#### Subsequent departures

A physical aircraft can operate multiple flights. With a selected destination,
the tracker watches for at least **ten minutes of corroborated surface activity**
within 3 nm of that airport, followed by a fresh airborne observation. Surface
fixes must be distinct, fresh, no more than 90 seconds apart, and at or below
80 kt when speed is available. Cached reports and long coverage gaps do not
establish the ground interval. A short ground interval or a go-around does not
trigger this next-leg guard, and a callsign change alone does not imply a new leg.

When triggered, the flight context becomes `subsequent_departure`. Aircraft
positions continue updating by the locked hex, while **previous-flight ETA and
arrival inference are disabled**. The dashboard and plain output explain that a
new session is needed for the next leg; the old reference destination is not
silently applied to the new departure. This is a heuristic, not a dated-flight
schedule lookup: incomplete ground coverage can prevent detection of a later leg.

### Live-data providers

Choose a live feed with `--provider adsb.lol` (default) or `--provider adsb.fi`.
Provider names are case-insensitive. The selection applies for the duration of
the command; restart with the other value to switch feeds. Failed requests retry
against the selected feed in continuous mode. Unknown provider names are rejected
before any network request.

| Provider | API base URL |
| --- | --- |
| `adsb.lol` | `https://api.adsb.lol` |
| `adsb.fi` | `https://opendata.adsb.fi/api` |

Both providers' public callsign, hex, and registration endpoints work without an API key and provide compatible
data for position tracking, touchdown estimates, and likely-landed detection.
Coverage and availability can differ. Flight-number resolution uses a matched
Swedavia record when available, otherwise adsbdb; airport geometry uses OurAirports. Terminal attribution,
JSON `source`, and live-feed errors identify the selected provider.

ADSB.fi permits personal, non-commercial use, requires attribution with a link to
its home page, and limits public endpoints to one request per second. The CLI's
minimum five-second interval fits this per-process limit; multiple concurrent
instances share your connection's request budget. See the
[ADSB.fi API documentation and terms](https://github.com/adsbfi/opendata).

### Terminal output

Each poll appends a readable snapshot, suitable for both a terminal and log
redirection. Available details include the reference route, registration,
aircraft type, position, map link, barometric altitude (feet), ground speed
(knots), ground track (degrees clockwise from north), vertical rate (feet per
minute), and contact/position timestamps with their ages. Ground track is the
direction of travel, not necessarily the aircraft's nose heading.

Missing speed/track/vertical-rate fields can be recovered from recent observations
and are marked with `~`. Reported values, including zero, retain priority; raw JSON
fields remain unchanged. Unrecoverable values are `unavailable`, not zero. A surface report is
shown as `on ground`; this alone does not establish arrival at the destination.

### Calculated measurements

Speed and direction fallbacks use distinct fresh positions spanning 10–90 seconds;
vertical trend uses at least three barometric altitude samples over 30–90 seconds.
The ETA and approach estimator can use them, while surface-based landing
corroboration keeps using reported evidence. The processor resets continuity on
tracking gaps, identity changes and air/ground transitions, and rejects implausible
jumps. No extra API calls are made.

Dashboard values carry `~`, `SPD~` marks calculated speed in a chart, and `?` shows
per-value calculation windows and methods. JSON retains raw fields and adds a
separate `calculated` object. See [measurement rules and provenance](docs/measurements.md).

### Radar dashboard

Add **`--tui`** for an interactive, full-screen single-flight dashboard with
green/cyan accents, bordered panels, and a deduplicated event timeline:

```text
╭─ AEROLATCH / RADAR VIEW ────────────────────────────────────────────╮
│ NSZ4366 · ADSB.fi                                           ● LIVE │
│ Norwegian Air Sweden AOC · SE-RTG · 4aca87                          │
│ Reference: VCE → ARN | Using ARN (adsbdb reference)                 │
╰───────────────────────────────────────────────────────────────────╯
╭─ FLIGHT DATA ─────────────────╮ ╭─ ARRIVAL / INFERRED ─────────────╮
│ SE-RTG · B38M                 │ │ Phase: approach                 │
│ Altitude: 300 ft              │ │ Distance: 2.7 nm (straight line)│
│ Speed: 125 kt · Track: 10 °   │ │ Touchdown: ~21:11 UTC (rough)    │
│ Vertical: -640 ft/min         │ │ Arrival: AIRBORNE               │
│ Distinct fixes (· = gap)      │ │ Surface: unavailable            │
│ ALT █▇▅▃▁                    │ │                                 │
│ SPD ▄▄▄▅▄                    │ │                                 │
╰──────────────────────────────╯ ╰─────────────────────────────────╯
```

The example is illustrative. The actual view adapts to terminal dimensions:

- **Flight data:** aircraft identity, altitude, ground speed/track, vertical rate,
  and sparklines. Trends use up to 60 retained points and display the latest points
  that fit (at most 20); a dot represents a missing value or coverage gap. Points
  are timestamp-distinct observations, not uniformly spaced time buckets. Cached
  and out-of-order fixes do not extend a trend. Known zero and negative barometric
  altitude remain valid values.
- **Arrival:** current or last observed phase, rough ETA, distance, arrival
  assessment, and evidence summaries. An expired or stale ETA is withheld. Possible
  and likely landings keep their different evidence levels; `?` shows full reasons
  and the last approach timestamp.
- **Position:** actual fix/contact ages update every second. Retained observations
  are labeled **NOT CURRENT**, stale fixes are marked **STALE**, and multiple matches
  are **AMBIGUOUS** rather than choosing an arbitrary aircraft. Trend and phase
  history reset when the aircraft identity changes.
- **Events:** acquisition, phase changes, tracking loss/recovery, feed errors, and
  arrival transitions. The latest 100 events are kept within the current session.

At 80×24 the main panels fit side by side; narrower terminals stack them. Page
scrolling makes remaining panels accessible in short/narrow terminals. Below
38×12, a resize prompt is displayed while tracking continues. Colors adapt to
terminal capabilities. The selected provider and its attribution remain visible.

| Key | Action |
| --- | --- |
| `q`, Ctrl+C | Quit and cancel the tracker/request |
| ↑ / `k`, ↓ / `j` | Scroll older/newer events |
| Page Up / Page Down | Scroll dashboard panels |
| Home / `g` | Return to the top and newest events |
| `?`, Esc | Toggle help / close help |

`--tui` requires terminal input and output and continuous tracking; combining it
with `--once` or `--json` is rejected before making network requests. The dashboard
opens after initial flight-number and destination lookups. A loading state is shown
while waiting for the first live snapshot. Screen ticks, resizing, help, and key
presses do not trigger API calls: the existing tracker controls polling and retries.

After a fatal tracking error the dashboard displays **STOPPED** for inspection;
press `q` to exit and return the error status. Exiting, cancellation, and signals
restore the terminal's normal screen, cursor, and input settings.

### Estimated touchdown

Flight-number tracking automatically includes a rough touchdown estimate when
the destination and live telemetry support one. No extra option or API is needed:

```text
Distance to destination: 180 nm (straight line)
Estimated touchdown: ~15:40 UTC, 17 Oct (about 35 min remaining)
Estimate: rough | Phase: descent — heuristic using selected destination; approach and routing may change
```

This is a local heuristic, **not an airline/airport ETA or a gate-arrival time**.
It uses the selected destination: your override, or otherwise the reference route,
which may differ from today's actual route.
It has not been calibrated against actual landings; `rough` is a qualitative
label, not a statistical confidence interval.

The model:

- Computes great-circle distance to the destination and keeps up to two minutes
  of position history, separately for each aircraft identity.
- Smooths ground speed using a median of timestamp-distinct observations. An
  initial estimate can be made from one fresh fix with a ground track; without
  track, at least 30 seconds of position history is needed.
- Classifies cruise, climb, descent, or approach using distance, altitude relative
  to airport elevation, and reported vertical rate. Barometric altitude minus
  airport elevation is only an approximate height: pressure differences are not
  corrected.
- Models the last 20 nm at no more than 160 kt and the preceding 80 nm at no more
  than 280 kt; remaining cruise distance uses smoothed speed. Near approach, the
  current speed is used for the final segment. These are fixed-wing heuristics,
  not a prediction of the assigned runway or flight path.
- Adds an altitude time budget using a nominal 1,500 ft/min descent rather than
  extrapolating the instantaneous vertical rate. A routing allowance of up to
  two minutes tapers toward zero near the airport.
- Anchors the prediction to the provider's position timestamp. Repeated cached
  fixes do not continuously push the same predicted touchdown into the future.

The CLI explains why an estimate is unavailable: stale positions (over 90 seconds),
missing destination/telemetry, surface reports, multiple matches, movement away
from the airport, little progress suggesting holding/vectoring, or climbing close
to the destination. Speeds outside 80–800 kt are outside this model. A short turn
can temporarily suppress an estimate; holding detection needs position history.
Unknown future ATC delays cannot be predicted.

`--once` supports an initial estimate. Direct callsign/hex/registration modes skip
route lookup; use `--destination` to enable estimates in those modes. During visibility gaps
or request failures, previous aircraft observations may remain visible, but a
previous ETA is never presented as current.

### Destination overrides

`--destination ALC` or `--destination LEAL` selects Alicante for both the ETA and
arrival detector. The original adsbdb route is retained and displayed separately,
so an override cannot silently rewrite the reference data. The terminal shows
`Destination used` with its source. Without an override, the adsbdb reference
destination is used; a wrong reference mapping can still produce a wrong assessment.

Overrides resolve airport metadata from the public-domain **OurAirports** CSV on
GitHub. The dataset is streamed once at startup per invocation (potentially tens
of megabytes); there is no API key, persistent cache, or additional disk file.
The normal `--timeout` applies to this download too. Unknown/ambiguous codes,
invalid metadata, and lookup failures stop the command rather than falling back
to a different destination. Closed facilities are excluded. Three-letter IATA
codes and four-letter ICAO/GPS codes are supported.

### Arrival assessments

Arrival inference has two evidence levels: **possible landing** after a recent
airborne-to-surface transition, or a low descending approach and sustained loss
of tracking; and **likely landed** after corroborating surface observations.
Both work automatically with either live-data
provider and require a destination and observations from the current run.

#### Likely landed: surface evidence

Continuous tracking automatically assesses arrival using fresh observations of
the same aircraft (identified by its ICAO hex address). Initial heuristics require:

1. An explicit airborne observation within 25 nm of the selected airport and
   approximately -1,500 to 8,000 ft relative to its elevation. The airborne
   observation must be within the preceding ten minutes.
2. An explicit surface report within **3 nm** of the airport, with ground speed
   no higher than **80 kt** if supplied. Missing speed is allowed; an explicit
   surface report is still required. Following the recent airborne observation,
   this first fresh surface report immediately establishes **possible landing**.
3. A second distinct surface position after a short **3-second debounce**
   establishes **likely landed**. The debounce rejects timestamp jitter without
   requiring prolonged ground reception: five seconds of distinct surface
   coverage is sufficient. Consecutive
   surface observations must be no more than 90 seconds apart. Position and
   available telemetry timestamps must be fresh (no more than 90 seconds old).

```text
Arrival assessment: likely landed at Alicante (ALC)
Evidence: airborne observation followed by repeated fresh surface reports near destination; inferred, not an airline status
First surface observation: 2026-10-16T22:59:10Z (not an exact touchdown time)
Corroborating surface observation: 2026-10-16T22:59:40Z
```

Starting on the ground, including with `--once`, reports **on ground near
destination; arrival unconfirmed**. Low speed, low altitude, a single surface
report without preceding airborne evidence, or disappearing from the feed do not
independently establish a landing.
Repeated cached positions and out-of-order fixes do not advance detection.

A previously inferred landing is retained during a coverage gap or request error
with a **Last inferred arrival assessment** label and its original evidence times.
Fresh airborne observations supersede it, including during a go-around or a new
departure. Aircraft identity changes or ambiguous matches reset the association.
Tracking continues until Ctrl+C; evidence is kept only within the current run.

The preceding approach and actual surface reports are kept together. Tracking
loss, stale replies, and API errors retain the existing evidence level and exact
observation timestamps; elapsed wall-clock time cannot promote a single surface
report to corroborated landing. Ground-only observations are also retained as
last observed facts, without inferring an arrival. The dashboard shows the number
and span of surface fixes and the last surface timestamp, rather than indefinitely
displaying “Gathering observations.”

The NSZ3235 Copenhagen regression reproduces the screenshots: surface fixes at
**21:26:01** and **21:26:06 UTC**, at 56 kt near CPH, following a recent approach.
The first is a possible landing and the second corroborates likely landing. Cached
polls add no reports. After tracking disappears, the dashboard retains:

```text
Last inferred: LIKELY LANDED
Retained surface: 2 fixes / 5s
Last surface: 21:26:06Z
```

#### Possible landing: approach followed by coverage loss

Some flights disappear from receiver coverage just before touchdown, so no
surface report arrives. The detector keeps a bounded two-minute history of
distinct, fresh airborne positions for the same aircraft. A qualifying approach
requires:

- At least two distinct positions spanning **30 seconds**, showing at least
  **100 ft of descent** and **0.5 nm of progress** toward the selected destination.
- Observations within 10 nm of the airport, with ground speed **80–200 kt** and
  approximate height between -1,500 and 5,000 ft relative to airport elevation.
  The final observation must be within **3 nm** and no more than **1,500 ft**
  above airport elevation. Negative barometric heights are tolerated as pressure
  discrepancies; they do not independently imply touchdown.
- No gap between approach fixes longer than 90 seconds. Climbing, movement away,
  and implausible position jumps invalidate the approach sequence.
- At least **two successful empty API responses**, and at least **two minutes
  since the last actual position timestamp**. A new inference must be made within
  ten minutes of that last fix; older approach evidence expires.

```text
Arrival assessment: possible landing at Stockholm-Arlanda Airport (ARN)
Evidence: low descending approach followed by sustained successful empty responses; surface confirmation unavailable
Last approach position: 2026-10-17T21:10:09Z (historical evidence, not a current position)
Approach evidence: 3 fixes over 40s; descended 475 ft; last distance 1.3 nm, speed 132 kt
Surface confirmation: unavailable
```

The NSZ4366 regression scenario uses fixes at 21:09:29, 21:09:59, and 21:10:09 UTC.
Cached polls do not add fixes. Successful empty responses at 21:11:31 and
21:12:01 are still too early; a further empty response at **21:12:31** first
produces `possible_landing`.

API failures and malformed responses do not count as empty observations and
break pending empty-response corroboration. A returned aircraft, even with only
a cached position, also breaks that sequence. Existing inferences can be retained
with a historical label while new corroboration is unavailable. Fresh observations
supersede the possibility: a go-around clears it, and subsequent corroborating
surface reports can establish `likely_landed`.

An approach-based possibility is less reliable than surface evidence. A coverage
gap during a go-around, vectoring, or a provider-side data interruption can look
similar. The historical approach coordinates and altitude are never promoted to
current telemetry, and neither an expired ETA nor disappearance alone is used as
proof of landing. `--once` cannot build either observation sequence.

The radius and thresholds are uncalibrated heuristics. Nearby airports, inaccurate
positions or surface flags, and incorrect destination mappings can affect the
result. The detector does not establish an exact touchdown time, gate arrival,
or cancellation, and it does not query an operational flight-status provider.

### JSON output

`--json` writes newline-delimited JSON to stdout. Each object contains:

- `checked_at`, `query`, `callsign`, `source`, and an optional `reference_route`.
  `source` is `ADSB.lol` or `ADSB.fi`, including during request failures.
- `tracking`: the actual `lookup` (`kind`: `callsign`, `hex`, or `registration`,
  plus its `value`), optional `locked_hex` and `locked_at`, `flight_state`
  (`tracking` or `subsequent_departure`), `live_lookup_performed`, and an explanatory
  `reason`. When a scheduled flight is waiting, its lookup is planned, not polled. The first
  acquiring snapshot still identifies its original lookup; subsequent polls
  use hex. For `--hex`, `locked_hex` is the configured address, while `locked_at`
  is only set after a unique recent observation. These are acquisition times,
  not flight departure times. Flight-number/callsign sessions retain their
  original top-level `callsign`; each `aircraft[].callsign` is the actual current
  broadcast value, which may be empty. Direct hex/registration sessions populate
  the top-level callsign from a current unique observation when available.
- `status`: `visible`, `not_visible`, `multiple_matches`, `retrying`, or `error`.
  Schedule-backed selection can also be `scheduled`, `waiting_for_schedule`,
  `cancelled`, `reported_landed`, or `deleted`.
- `aircraft`: current matching aircraft, or `[]` when no current result exists.
  Each aircraft can include a `calculated` object for missing-measurement fallbacks;
  its raw measurement fields remain reported values or `null`.
- `last_observed` and `last_seen_at`, when retaining a previous observation
  during a visibility gap or failed request. These are **not current positions**.
- `error` and `retry_at` when relevant.
- `landing_estimate`: `status` (`estimated` or `unavailable`), an explanatory
  `reason`, straight-line `distance_nm`, and `based_on_position_at`. When estimated,
  also includes `touchdown_at` (UTC), `minutes_remaining`, `phase`, `quality`
  (`rough`), and `smoothed_ground_speed_kt`. Unavailable predictions have null
  touchdown/remaining-time values rather than a retained ETA.
  `calculated_inputs` identifies fallback measurements supplied to the model.
- `destination`: the selected `airport`, `source` (`user_override`, `adsbdb`, or `swedavia`),
  and metadata `data_source`. `reference_route` remains unchanged by overrides.
- `arrival_assessment`: `status` (`unknown`, `airborne`, `possible_landing`,
  `on_ground_near_destination`, or `likely_landed`), `reason`, `aircraft_hex`,
  `distance_nm`, and `historical`. Evidence timestamps, when available, are
  `last_airborne_at`, `first_surface_at`, `last_surface_at`, `corroborated_at`, and
  `last_observed_at`. `surface_reports` counts distinct accepted surface fixes;
  cached/stale/out-of-order replies do not increment it. Surface-based inferences
  include `assessed_at`, the detection time of the initial surface possibility,
  while `corroborated_at` is the actual qualifying surface-fix time. Neither is
  presented as an exact touchdown time.
  A historical assessment describes previous evidence, not the current aircraft
  position. Feed visibility remains a separate top-level `status`.
- Approach-based assessments additionally expose `approach_evidence` with
  `started_at`, `last_position_at`, `observation_count`, `last_distance_nm`,
  `last_height_ft`, `last_ground_speed_kt`, `altitude_drop_ft`, and `progress_nm`.
  `missing_since` is the first successful empty-response time in the current
  corroboration sequence, `empty_responses` counts distinct successful empty
  polls, and `assessed_at` records when the possible landing was first inferred
  (not touchdown). Approach evidence is always historical, even when a new
  successful empty lookup supports a current assessment (`historical: false`).
- `airport_report` and `route_source` identify optional Swedavia records and dated
  route provenance; see [the airport-report schema](docs/swedavia.md#json-fields).

Unknown aircraft measurements are JSON `null`; known zeros remain numeric zero.
Times use RFC 3339 in UTC. Contact and position times are calculated from the
provider's feed timestamp and reported ages, rather than the request time.
Check those timestamps: a visible feed entry can still have an old position.
`last_seen_at` records when this process last fetched a matching aircraft.

Multiple aircraft with the same callsign are all returned with
`multiple_matches`; the CLI does not pick an aircraft arbitrarily.

### Errors and retries

- Continuous tracking backs off on network failures, invalid responses,
  HTTP 408/429, and server errors. Exponential backoff starts at the requested
  interval and grows to five minutes (or the interval if longer). A provider's
  longer `Retry-After` takes precedence. Success resets the backoff.
- Other HTTP client errors stop tracking. Initial flight-number resolution
  errors also stop the command; an unknown mapping can be bypassed with
  `--callsign`, `--hex`, or `--registration` when the aircraft identifier is known.
- `--once` never retries. A successful lookup with no aircraft exits 0 and reports
  `not_visible`. Invalid arguments, failed API requests, and output failures
  exit nonzero. Ctrl+C/SIGTERM stops cleanly.
- In JSON mode, live lookup failures are emitted as JSON snapshots; fatal
  diagnostics (including resolution failures) go to stderr.

## Coverage and scope

The CLI tracks live observations and assesses possible/likely landings from a
tracking session. Optional Swedavia data adds schedules and reported times/statuses
for covered dated legs within its available window; it is not a global history
database. Last ADS-B observations and inferred arrivals are retained only in memory
during the current run.

Free ADS-B reception is incomplete, especially at low altitudes and over oceans.
No live match means **not currently visible**, not confirmed landed or cancelled.
Flight-number mappings are best-effort reference data, not dated schedules:
codeshares, reused flight numbers, and alphanumeric operational callsigns can
prevent a match. A resolved route may be outdated or differ from today's flight.

The provider clients, observation model, polling logic, and output are separate
packages so a historical/arrival-status provider can be added later.

## Data sources and attribution

- [adsbdb](https://www.adsbdb.com/): passenger flight-number/callsign mapping and
  airline/route reference data. Flight-route data is the work of David Taylor,
  Edinburgh, and Jim Mason, Glasgow. See the
  [project and data notices](https://github.com/mrjackwills/adsbdb). This CLI queries
  the public API on demand and does not bundle a route database.
- [ADSB.lol](https://www.adsb.lol/docs/open-data/api/): live aircraft observations,
  made available under [ODbL 1.0](https://opendatacommons.org/licenses/odbl/1-0/).
  Please retain appropriate attribution when sharing exported data.
- [ADSB.fi](https://adsb.fi): alternative live aircraft observations. See its
  [API documentation and terms](https://github.com/adsbfi/opendata) for personal,
  non-commercial use, attribution requirements, and request limits. Retain the
  source attribution and a link to https://adsb.fi when sharing its data.
- [OurAirports](https://github.com/davidmegginson/ourairports-data): airport
  metadata for destination overrides; public-domain airport data. Downloads use
  the project's GitHub mirror and do not contain current flight status.
- Map links open [OpenStreetMap](https://www.openstreetmap.org/copyright).
- [Swedavia](https://www.swedavia.se): optional dated flight boards, operational
  assignments and reported airport statuses, subject to its API terms. Requires
  a [developer subscription](https://apideveloper.swedavia.se/).
- The interactive dashboard is built with [Bubble Tea](https://github.com/charmbracelet/bubbletea)
  and [Lip Gloss](https://github.com/charmbracelet/lipgloss), both MIT-licensed.

These public community services can change availability and limits. The CLI uses
targeted callsign/registration queries for acquisition and targeted hex queries
for continued tracking, rather than downloading all aircraft.

## Development

```sh
go test -race ./...
go vet ./...
go build -o aerolatch .
```

Tests use local HTTP fixtures and a mocked CLI transport; they require no external
API access. Dashboard tests cover model updates, responsive layouts, timestamps,
trend gaps, events, cancellation, and CLI validation. `TestDashboardTerminalHelper`
is an opt-in process fixture for pseudo-terminal checks, enabled with
`AEROLATCH_TUI_TEST=q|ctrlc|cancel|fatal|calculated` when the test binary is run in a PTY.
Live smoke tests can be run separately using the usage commands above.
