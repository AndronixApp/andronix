# Installer telemetry: events and properties

What the installer sends (opt-out: `andronix telemetry off`; only plain x.y.z release builds send anything). products-api allow-lists every property; anything else is dropped there. Schema v2 (`backend/docs/telemetry-schema-v2.md`) is marked **v2**, from installer 2.1.

## On every event

`arch`, `android_sdk`, `termux_flavor` (f_droid, github, google_play_store, unknown; empty outside Termux), `version`, `host` (termux, standalone, linux, distro), and in Termux `termux_version`, `termux_pkg`, `applib` (Play Store Termux's link to its libraries: link_ok, link_bad, dir, missing; only when there is one or the flavor is Play, support #358) and `termux_env` ("missing" when andronix runs without Termux's session environment: su, SSH, env -i, Tasker; the flavor then comes from the applib link, else unknown). The envelope has the random `install_id` (per installation of andronix, not per run) and `ts`.

## Events

| event | properties |
|---|---|
| `install_started` | `distro`, `de`, `modded`, `edition`, **v2** `attempt_id`, `gen_id` |
| `install_result` | `ok`, `distro`, `de`, `modded`, `duration_ms` (machine time: **v2** without the interactive user setup), `outcome`, `source`, `fallback_reason`, `download_bytes`, `download_ms`, `unpack_ms`; on failure `step` (label), `error_class`, `error_kind`, `error_detail`; on a saved run `error_kind` = the recovery. **v2**: `attempt_id`, `gen_id`, `steps_ms`, `step_key` (failure), `user_setup_ms`, and always `proot_version`, `linker`, `kernel`, `rom`, `ram`, `probe`, `rules` |
| `start` | kept for one release next to `cli_login` and `desktop_start`: the command line (`distro`, `de`, `root`, `command`), Termux:X11 (`distro`, `de`, `x11_args`, `kernel`, `drew`) or the app's display (`distro`, `de`, `display`, `ok`, `duration_ms`, `sound`, `gpu`, `gpu_ok`, `error_class`) |
| **v2** `cli_login` | `distro`, `de`, `root`, `command` |
| **v2** `desktop_start` | `kind` (x11, display, vnc) and the `start` properties of that path; the first desktop start after an install adds `first: true` and that install's `attempt_id` |
| `update` | `ok`, `distros`, `self_only`, `error_class`, `duration_ms`; `proot_version`, `linker` on package-manager failures; `error_kind` on a saved run |
| `pack_add` | `packs`, `distro`, `ok`, `error_class`, `duration_ms` |
| `compat` | after a probe: `probe`, `kernel`, `sdk`, `rom`, `phantom`, `ram`, `termux`, `rules`, `distro`, `de` |

## Formats (v2)

- `attempt_id`: 32 lower-case hex, random, new for every `andronix install` run. Install success = share of `attempt_id`s with `install_result` ok.
- `gen_id`: 16 lower-case hex, the app's per-command `ANDRONIX_INSTALL_ID` (only when it is exactly 16 hex); joins `install_generated` in the app.
- `steps_ms`: `key:ms` pairs joined by commas, in run order, at most 20 (+ `user` for `--user`), keys from docs/progress-json.md's step keys, e.g. `phone-check:812,find-download:640,download:24177,unpack:572,setup:9120,refresh:15022,update:81234,desktop:240110,browser:51022,desktop-setup:40211,finish:900`. The failed step is the last pair.
- `step_key`: the failed step's key (`refresh`), next to its label `step` ("Refreshing package lists").
- `user_setup_ms`: integer ms in the interactive Username/password questions.
- `kernel` major.minor, `rom` family, `ram` bucket (`<3G`, `3-4G`, `4-6G`, `6G+`), `probe` summary (`not-probed` before the setup step, `all-ok`, or `name:result` pairs), `rules` space-separated compat rule ids.
- `first`: boolean, only `true` (absent otherwise).

No personal data: ids are random, timings only, kernel/ROM/RAM coarse, no IPs.
