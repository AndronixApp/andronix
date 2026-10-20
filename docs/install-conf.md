# `install.conf`: what the app reads about an installed distro

Each installed distro has `$ANDRONIX_HOME/distros/<id>/install.conf`. Under Termux that's `~/.andronix/distros/<id>/install.conf`. The Andronix app reads it for its "Your Linux" list.

The file is plain `KEY=value` lines with no quoting, and the value runs to the end of the line. The installer replaces it atomically (it writes a temporary file, then renames it).

These keys are a contract. They keep their names and meanings, and new keys may be added.

| key | meaning |
|---|---|
| `DISTRO` | the distro ID (`debian`, `ubuntu24`, …), the same as the folder name |
| `VERSION` | the distro's version (`13`, `24.04`, `rolling`) |
| `EDITION` | the Modded or Classic edition ID; empty for a plain distro |
| `DE_NAME` | the desktop's display name (`XFCE`, `Command line` for none) |
| `STAGE` | how far the install got. Only `done` means finished. Earlier values are `extracted`, `configured` and `desktop`, for an unfinished install that `andronix install` resumes. |
| `USER` | the user Andronix logs in as. Empty means root, or a user still to be created at first login. |

Read unknown keys as opaque. Don't write to the file: the installer owns it.
