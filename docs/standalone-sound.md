# Sound in the standalone app

In the Andronix app (`ANDRONIX_HOST=standalone`) there's no Termux PulseAudio, so the distro runs its own.

`andronix display <distro>` and `andronix vnc <distro>` start it before the desktop:

1. If the distro has no `/usr/bin/pulseaudio`, they install `DISTRO_PULSE` (from `distros/<id>.conf`) once. This is the first start only.
2. Inside the distro, `andronix sound start` runs PulseAudio with a silent sink, `andronix`. Its monitor is served raw on a Unix socket.
3. Once that socket answers, they print one line on stdout, **before** the `DISPLAY ready` or `VNC ready` line:

```
SOUND ready pcm:<ANDRONIX_HOME>/d/snd rate:48000 channels:2 format:s16le
```

The app connects to `pcm:` and reads interleaved signed 16-bit little-endian stereo PCM at 48 kHz, for as long as the session runs. It plays that with AAudio. PulseAudio sends silence while nothing plays, so the stream doesn't stall.

If there's no `SOUND` line, there's no sound. The desktop still starts. Why is in `~/.andronix/logs/display-<distro>.log` (or `vnc-<distro>.log`), and inside the distro in `/tmp/.andronix-pulse/pulse.log`.

Programs in the distro find the server through `PULSE_SERVER=unix:/tmp/.andronix-pulse/native`, which andronix sets in the session and in `andronix start`.

Under Termux nothing changes: there is no `SOUND` line, and `PULSE_SERVER` stays `127.0.0.1` (Termux's PulseAudio).
