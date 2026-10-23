# Calculated fallback measurements

Tracking automatically calculates missing ground speed, ground track, and a
barometric vertical trend from recent live observations. This is local math:
no additional API requests, subscription keys, or dependencies are needed.

Reported measurements always win, including valid zero values. Absolute altitude,
position, contact times, and air/ground flags remain feed measurements. A missing
altitude is not reconstructed from latitude/longitude.

## Observation requirements

The processor follows one unique ICAO hex address, independent of callsign. It
retains up to 64 distinct observations within two minutes, but calculations use
only fresh observations no more than 90 seconds old. Fixes/available contact times
more than five seconds ahead of the local clock are rejected.

- **Ground speed:** at least two fixes spanning 10–90 seconds. Sum great-circle
  distances between observations and divide by elapsed hours for knots.
- **Ground track:** final great-circle bearing of recent displacement, clockwise
  from true north. This is direction of recent movement, not nose or magnetic
  heading. Suppress direction below 0.03 nm displacement or 5 kt, including when
  reported speed says the aircraft is slower than 5 kt.
- **Vertical rate:** at least three finite barometric altitude samples spanning
  30–90 seconds, including altitude at the latest accepted fix. Fit altitude against
  elapsed time with centered least squares and convert the slope to feet/minute.
  This is an approximate altitude-derived trend, not instantaneous vertical speed.
  The feeds do not timestamp altitude independently, so positions act as sampling
  times. Cached fixes do not become new altitude samples when fields change.

At the default 30-second polling interval, speed/track normally become available
after the second suitable fix and vertical trend after the third (about a minute).
`--once` has no prior observation history and normally cannot calculate fallbacks.

## Quality and continuity

Repeated cached positions add no samples and never refresh calculation age.
An existing valid window can be reused while its samples remain fresh; its original
end timestamp is preserved. Stale/out-of-order or invalid observations withhold
calculations. Empty results, request failures, scheduled waiting, and ambiguous
matches break numeric continuity; recovery requires new distinct fixes. A cached
pre-gap fix cannot become a new baseline just because it reappears.

Aircraft changes and air/ground-state transitions reset the history. The first
surface fix cannot inherit averaged approach speed. Ground movement may subsequently
be calculated from ground observations, but no vertical trend is calculated on the
surface. Calculated low speed never creates an on-ground flag.

Consecutive position jumps above 1,200 kt are rejected and restart collection.
Altitude jumps/trends above 12,000 ft/min discard altitude continuity without
discarding otherwise valid horizontal movement. These are plausibility limits,
not calibrated error bounds; GPS noise, turns, barometric pressure, and sampling
gaps still affect accuracy. Raw feed values are preserved even when unusable by
an estimator.

## Consumers and display

The dashboard and plain output use effective values: reported first, calculated
only when the reported field is missing. Calculated numbers carry `~`:

```text
Ground speed: ~144 kt (calculated, 30s)
Ground track: ~90 ° (calculated, 30s)
Vertical rate: ~-1000 ft/min (calculated, 1m0s)
```

The compact dashboard uses `~144 kt`, with a `~ calc` legend. `SPD~` indicates that
the displayed speed chart includes calculated points. Missing values and coverage
gaps remain dots, not interpolated or zero-filled measurements. `?` lists sources,
observation windows, counts, methods, ages, and reasons for unavailable fallbacks.

ETA and approach-only arrival assessment can use calculated speed/track/vertical
trend and expose `calculated_inputs` in their results. An approach records whether
its last speed was reported or calculated. These remain rough/inferred results.

**Surface-based landing corroboration and the subsequent-departure guard continue
to use reported fields.** Explicit surface reports remain necessary. A calculated
average speed cannot override reported speed, disqualify an otherwise accepted
missing-speed surface report, fabricate touchdown, or change airport-reported status.

## JSON provenance

Existing raw fields retain their values or `null`:

```json
{
  "ground_speed_kt": null,
  "ground_track_deg": null,
  "vertical_rate_ft_min": null,
  "calculated": {
    "ground_speed": {
      "value": 144.1,
      "unit": "kt",
      "window_start": "2026-10-20T12:00:30Z",
      "window_end": "2026-10-20T12:01:00Z",
      "samples": 2,
      "method": "position_path_distance / elapsed_time"
    }
  }
}
```

`calculated` may contain `ground_speed`, `ground_track`, `vertical_rate`, and an
`unavailable` map explaining missing fallbacks. Each measurement has its own window
and sample count: horizontal and altitude windows can differ. `window_end`, not
fetch time, determines age. No numeric fallback is emitted with insufficient data.
If all fields are reported, the calculated object is normally omitted.

Historical `last_observed` aircraft retain their original calculation provenance;
their values are not promoted to current telemetry. History exists only during
the current tracking session.
