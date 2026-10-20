# `--progress json`: the installer's machine-readable progress (v1)

For the Andronix app's own install screen. With `--progress json`, the installer writes one JSON object per line to **stdout**: nothing else, ever. Everything meant for people (banners, boxes, apt output) goes to **stderr**. Fields may be added within v1; existing ones keep their meaning. A breaking change bumps `v` in `hello`.

```sh
libandronix.so install debian --de xfce --yes --user alex --progress json
```

`--progress json` implies `--yes` and no questions.

## Events (`t`)

| t | fields | when |
|---|---|---|
| `hello` | `v` (1), `installer` (e.g. `2.1.0`), `host` (`termux` \| `standalone` \| `distro` \| `linux`), `command` | first line |
| `step` | `n`, `of`, `key`, `label` | a step starts |
| `progress` | `n`, `cur`, `total`, `unit` (`bytes` \| `packages` \| `""`) | at most 4 per second per step |
| `ok` | `n`, `key`, `label`, `secs`, `detail`, `count`, `detail_key`, `detail_args` | a step finished. `count` is the number at the start of `detail` ("252 packages" gives 252), when there is one. `detail_key` and `detail_args` (2.1.1+) say what `detail` is, for translation; see Step details |
| `skip` | `n`, `key`, `label`, `secs`, `reason` | a step wasn't needed |
| `fail` | `n`, `key`, `label`, `secs` | a step failed; an `error` follows |
| `note`, `warn` | `text`, `key` | messages for the user (English); `key` when the app may show its own text (list below) |
| `error` | `title`, `what`, `fix`, `class`, `kind`, `detail`, `log` | the command failed |
| `done` | `status` (`ok` \| `fail` \| `cancelled`), `distro`, `edition`, `outcome` | last line |

- `label`, `text`, `title`, `what` and `fix` are English: the fallback when the app has no translation for the `key` or `class`.
- `label` in `ok` and `skip` can differ from the `step` event's: the step renames itself once it knows more ("Installing Firefox ESR").
- `outcome`: `already_installed` when nothing was installed because it already was, together with `status: ok`.
- `edition`: present for a Modded or Classic edition.
- The run's log (the `error` event's `log`) has the same events, each on a `progress: {json}` line, from `hello` on; `progress` events there at most every 10 s. Problem reports attach it.

## Note and warning keys

| key | when |
|---|---|
| `install_time` | the install has started and takes a while |
| `corrected_distro` | a typed distro name was corrected ("'debia' isn't a distro name; using Debian 13.") |
| `corrected_desktop` | a desktop name was corrected or replaced ("'xfc' isn't a desktop name; installing XFCE.") |
| `low_ram` | the desktop wants more RAM than the phone has |
| `old_kernel_desktop` | kernel before 4.8: desktops stay black (it suggests `--de none`) |
| `phantom_process` | Termux only; never in standalone |

Notes without a `key` are plain information. In standalone there's no telemetry notice: the app asks for consent itself.

## Step keys (install)

| key | English label |
|---|---|
| `proot` | Installing proot (Termux only) |
| `phone-check` | Checking your phone |
| `find-download` | Finding the best download |
| `download` | Downloading \<distro\> |
| `unpack` | Unpacking files |
| `setup` | Setting up \<distro\> |
| `refresh` | Refreshing package lists |
| `update` | Updating \<distro\> |
| `desktop` | Installing \<desktop\> desktop |
| `basics` | Installing the basics (with `--de none`) |
| `browser` | Installing a web browser |
| `desktop-setup` | Setting up the desktop |
| `finish` | Finishing up (standalone) / Writing start-\<distro\>.sh (Termux) |
| `user` | Setting up \<user\> (with `--user`) |

## Restore (moving a distro from Termux into the app)

In Termux: `andronix backup <distro> --to storage` writes `andronix-<distro>-<date>.tar.gz` to shared storage. The app lets the user pick that file and streams it in:

```sh
libandronix.so restore - --bytes <file size> --progress json < picked file
```

| key | English label |
|---|---|
| `restore` | Restoring \<distro\> (`progress` in bytes, against `--bytes`) |
| `finish` | Finishing up |

`done` carries `distro`. If the distro is already installed, nothing happens unless `--replace` is given (class `restore_would_replace`). A stream that isn't an Andronix backup fails with `not_a_backup`. A failed unpack (`restore_failed`, for example when the disk is full) removes the half-restored distro.

Other commands (`update`, `pack`) use keys made from their English labels until they get fixed ones.

## `selfcheck --json`

`libandronix.so selfcheck --json` prints one object: `v` (1), `ok`, `sdk`, `page_size`, `checks` (`api26`, `tmp_dir`, `exec_libdir`, `mmap_exec_data`, `anon_rw_to_rx`, `ptrace_child`, `proot_run`, each `{ok, ms, err}`), and, from 2.1, for problem reports:

- `probe`: what the compat engine knows without a distro: `summary` (`not-probed`, `all-ok`, or the syscalls that aren't ok, e.g. `openat2:ENOSYS,statx:EPERM`, from the last install's probe), `kernel` (major.minor), `sdk`, `rom` (lineage, miui, oneui, stock, emulator, other), `ram` (`<3G`, `3-4G`, `4-6G`, `6G+`), `termux` (Termux only).
- `rules`: the ids of the compat rules that match (`[]` when none). Rules that depend on a distro aren't in it.

Exit status 0 only when every check passes.

## Error classes (`class`)

Every error the installer explains has a fixed class: titles change, classes don't. Classes and kinds are only ever added, never renamed; a unit test (`internal/app/errclass_test.go`) fails when an error has no class or its class or kind isn't listed here. A class that isn't listed comes from an older installer, which used a slug of the title (`unknown_distro_xyz` and the like, before 2.1).

### Install

| class | meaning |
|---|---|
| `cancelled` | stopped by the user |
| `not_enough_space` | free space below what's needed, before or during the install |
| `package_manager` | apt, dnf, pacman, xbps or apk failed after all recovery |
| `termux_pkg` | installing a Termux package (proot, Termux:X11) failed |
| `proot_is_missing` | no usable proot |
| `proot_exec` | proot can't start programs inside the distro ("Linux can't start on this phone") |
| `termux_applib` | Play Store Termux's applib link (to its own libraries, which its proot needs to start programs) is broken: remove it and restart Termux (support #358). Termux only; kind `proot_exec` when proot already failed |
| `compat_refused` | a compat rule says this distro can't run on this phone yet |
| `kernel_too_new` | the distro needs an older kernel than the phone's |
| `no_build_for_your_cpu` | the distro has no build for this CPU |
| `not_android` | Termux userland off a phone (Android's version unreadable) |
| `unknown_distro` | no such distro |
| `which_distro` | no distro named and none to pick |
| `unknown_desktop` | no such desktop, or one Andronix doesn't offer (GNOME) |
| `desktop_not_offered` | the desktop isn't available on this distro (KDE on Arch) |
| `unknown_edition` | no such Modded edition |
| `that_edition_doesn_t_match` | the edition's distro or desktop isn't the one asked for |
| `early_access` | an early-access distro or edition needs Premium |
| `couldn_t_delete_the_old_files` | a replace couldn't delete the old install (a session still running) |
| `download_is_damaged` | the download's checksum doesn't match |
| `unpacking_failed` | unpacking the image failed |
| `setup_failed` | setting up the unpacked distro failed |
| `desktop_setup_failed` | the desktop's setup step failed |
| `couldn_t_write_the_start_script` | the start script couldn't be written |
| `bad_user` | `--user` name or password invalid |
| `setup_user` | creating the user failed |
| `not_on_a_phone`, `no_desktop` | `--image-build` misuse (build hosts only); `no_desktop` is also a desktop command on a distro without one |

### Downloads and Modded

| class | meaning |
|---|---|
| `download` | the image download failed (`kind` says why) |
| `download_not_available` | the image isn't reachable right now |
| `couldn_t_reach_the_download_servers` | no download server answered |
| `the_image_server_is_busy` | the image server is overloaded |
| `rootfs_file_not_found` | `ANDRONIX_ROOTFS` points at a missing file (tests) |
| `token_refused` | the Modded download link expired or was refused |
| `token_other_edition` | the token is for another edition |
| `missing_download_token` | a Modded install or update without a token |
| `that_token_doesn_t_look_right` | the token isn't in the expected form |
| `download_check_failed` | the server's checksum for an edition isn't valid |

### Start, remove, backup, restore

| class | meaning |
|---|---|
| `not_installed` | the distro (or edition) isn't installed |
| `not_finished` | the distro's install didn't finish |
| `install_running` | another andronix is installing, updating, removing or restoring the same distro (another Termux session); wait for it |
| `remove_failed` | removing left files behind |
| `couldn_t_remove_the_older_install` | the older install's files couldn't be removed |
| `couldn_t_remove_everything`, `couldn_t_remove_the_old_copy` | removing an edition left files behind |
| `backup_failed` | writing the backup failed |
| `backup_not_found`, `no_backups_found` | no backup at the given place |
| `no_access_to_phone_storage`, `can_t_write_to_phone_storage` | shared storage isn't granted or isn't writable |
| `not_a_backup` | `restore`: the stream isn't an Andronix backup |
| `restore_would_replace` | `restore`: the distro is installed; pass `--replace` to replace it |
| `restore_failed` | `restore`: unpacking failed (often space) |

### Desktop and display

| class | meaning |
|---|---|
| `no_desktop_installed_yet` | `andronix desktop` with no desktop installed |
| `another_desktop_is_on_termux_x11` | Termux:X11 is showing another distro's desktop |
| `user_setup_didn_t_finish` | the first-start user setup didn't finish |
| `the_desktop_session_is_missing` | the desktop's session script is missing |
| `the_desktop_didn_t_start` | the desktop (Termux:X11 or VNC) didn't come up |
| `the_desktop_session_stopped` | the desktop session ended right after starting |
| `x11_app_missing` | the Termux:X11 app isn't installed |
| `termux_x11_doesn_t_match` | the Termux:X11 app and package come from different builds |
| `termux_x11_needs_android_7` | Termux:X11 needs Android 7+ |
| `termux_x11_didn_t_start` | the Termux:X11 server didn't start |
| `vnc_isn_t_installed` | the distro has no VNC server |
| `vnc_is_already_running` | a VNC server already runs for this display |
| `no_password_set`, `couldn_t_save_the_password`, `no_vnc_password` | the VNC password is missing or couldn't be saved |
| `bad_display`, `bad_screen_size`, `bad_maximum_size`, `bad_dpi`, `bad_vnc_port` | an invalid display option |
| `the_vnc_socket_path_is_too_long`, `the_display_socket_path_is_too_long` | the app's socket path exceeds the Unix limit |
| `gpu_virgl_needs_the_server_s_socket`, `bad_gpu_socket`, `unknown_gpu_mode` | an invalid `--gpu` option (standalone) |
| `the_display_is_already_running`, `display_in_use` | the app's display (or that display number) is in use |
| `the_display_didn_t_start` | the app's built-in display didn't start |
| `guest_update_failed` | updating andronix inside the distro before the display failed |
| `no_xvfb`, `xvfb_isn_t_installed`, `couldn_t_install_xvfb` | the built-in display's Xvfb is unknown, missing or failed to install |
| `pulseaudio_isn_t_installed`, `sound_didn_t_start` | sound in the app (standalone) |
| `no_desktop_to_tune`, `unknown_profile`, `couldn_t_change_the_profile` | `andronix tune` |

### Update

| class | meaning |
|---|---|
| `unknown_channel` | no such update channel |
| `update_is_damaged` | the new andronix (or Modded update) failed its checksum |
| `couldn_t_replace_andronix` | the new andronix couldn't replace the old one |
| `beta_needs_a_token`, `beta_refused`, `beta_none` | the beta channel: no token, not Premium, or no beta right now |
| `couldn_t_read_the_update_list`, `the_update_list_is_damaged`, `wrong_update_list`, `no_update_path` | a Modded edition's update list |
| `couldn_t_apply_the_update` | applying a Modded update failed |

### Inside the distro (setup-user, packs)

| class | meaning |
|---|---|
| `run_this_as_root`, `can_t_set_up_that_user` | `setup-user` misuse |
| `unknown_time_zone`, `unknown_keyboard_layout` | an invalid `ANDRONIX_KEYBOARD` (`unknown_time_zone`: up to 2.1.0; since 2.1.1 a zone the distro lacks keeps the current one, with a line on stderr) |
| `couldn_t_create_the_user`, `couldn_t_set_the_password` | `setup-user` failed |
| `unknown_package_manager`, `packs_need_root`, `which_pack`, `unknown_pack` | `andronix pack` misuse |
| `pack_not_offered` | the pack isn't available on this distro |
| `pack_check_failed`, `pack_setup_failed` | a pack installed but its check or setup failed |

### Reports

| class | meaning |
|---|---|
| `not_a_report_id`, `no_description` | `andronix report` misuse |
| `report_not_found`, `report_expired`, `report_already_sent` | a report draft from the app |
| `couldn_t_open_the_andronix_app` | the draft couldn't be handed to the app |
| `couldn_t_send_the_report`, `too_many_reports`, `reports_aren_t_open_yet` | sending failed, was rate-limited, or reports aren't open |

### Command line

| class | meaning |
|---|---|
| `unknown_command`, `usage` | a mistyped command (never from the app) |
| `needs_a_terminal` | a question was asked with no terminal on stdin (never with `--progress json` or `--yes`) |

## Error kinds (`kind`)

Why it failed, for `package_manager`, `termux_pkg`, `download`, `proot_exec` and space. Only added, never renamed.

Package manager: `network_dns`, `network_connect`, `network_intercepted` (a captive portal or proxy answered instead of the mirror), `http_404`, `http_5xx`, `hash_mismatch`, `clock_skew`, `gpg`, `keyring` (the distro's signing keys can't be read), `fake_root` (a program in the distro wasn't root under proot; one retry with `PROOT_NO_SECCOMP=1`, and `fake_root_not_fixed` when that didn't help), `proot_syscall` (a program in the distro got "Function not implemented" or gpgv died under proot; one retry with `PROOT_NO_SECCOMP=1`, and `proot_syscall_not_fixed` when that didn't help), `dpkg_interrupted`, `dpkg_reinstreq`, `mirror_unusable`, `apt_hook` (an apt update or dpkg hook failed), `lock`, `broken_deps`, `disk_full`, `killed`, `cancelled`, `other`.

proot: `proot_exec` (it can't start programs in the distro), `proot_args` (it refused an option), `proot_failed` (any other proot error).

Downloads (class `download`): `http_<code>` (the HTTP status, e.g. `http_403`), `network_dns`, `network_connect`, `timeout`, `interrupted`, `checksum`, `disk_full`, `other`.

Desktop: `x11_app_missing`.

`detail` is the failing package-manager line, cleaned: no URLs (only their host), addresses or paths, at most 120 characters.

## Step details (2.1.1+)

An `ok` event whose `detail` the app can translate also has `detail_key` and `detail_args` (an object; `{}` when there are none). `detail` stays the English text, exactly as in 2.1.0, for older apps. Numbers in `detail_args` are JSON integers (sizes in bytes; format them yourself) and the rest strings. Keys and argument names never change once shipped: a change is a new key.

| `detail_key` | `detail_args` | `detail` (English) |
|---|---|---|
| `source.image` | `bytes` | ready-made image, 205.9 MB |
| `source.mirror` | `bytes` | Andronix mirror, 23.0 MB |
| `source.upstream` | `distro` (display name), `distro_id`, `bytes` | Arch Linux's own download, 131.3 MB |
| `source.official` | `bytes` | official image, 30.1 MB |
| `source.local` | | local file |
| `source.edition` | `edition` | the Modded edition's name |
| `verify.image` | | ready-made image, sha256 checked |
| `verify.sha256` | | sha256 checked |
| `verify.digests` | | digests checked |
| `arch` | `arch` (aarch64, arm, x86_64, i686) | ARM 64-bit |
| `arch.low_ram` | `arch`, `ram_mb` | ARM 64-bit · only 2900 MB RAM |
| `packages` | `count` | 252 packages (install, update, packs) |
| `desktop.light` | `ram_mb` | light profile (2900 MB RAM) |
| `links.repaired` | `count` | 12 links repaired |
| `update.version` | `from`, `to` | 2.1.0 → 2.1.1 |
| `update.installed` | | new version installed |

Not keyed: the sound and Termux:X11 display details, which only appear in Termux. `ui.Bytes` sizes are binary (1 MB = 2^20 bytes) but labelled MB, kept for the text the app matches.
