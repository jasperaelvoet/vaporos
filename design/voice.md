# VaporOS voice (REDLINE)

One voice sheet for the website, the control center and the TV. REDLINE is a thermal
image of the product: the box's state is its temperature, and white-hot is always the
thing you touch. The voice follows the same idea. It can run warm in a few named places,
and everywhere else it stays plain.

## What may change with the direction, and what never does

| May take the REDLINE voice | Never changes voice (plain, always) |
|---|---|
| Website headlines and chapter titles | Errors: what happened, then what to do, at most two sentences |
| The control center's hero word and its second line | Confirm dialogs: the question, the consequence, the two buttons |
| Scenes: the reconnection scene, Asleep, the installer's done state | Labels, buttons, field names, tabs, table headers |
| The TV's status line, as it already reads ("Ready to stream") | Numbers, versions, addresses, PINs, modes (`2560 × 1440 · 120 Hz`) |
| Empty states with nothing to fix | Anything a screen reader announces as a live region |

The terms table and case rules in the control-center spec (SCN §0.2) apply everywhere:
sentence case, no final period on buttons, "you" for the reader, a detail line of about
12 words at most, and the user-facing words ("stream server", "resolution", "restart to
update"), never the internal ones.

## The REDLINE rules

1. **Heat is a picture, never a number we invent.** Say "hot", "cools", "heats up" only in
   headlines and scenes. The only °C values anywhere are real sensor readings.
2. **Cold means a fault, and only a fault.** The cold colour always comes with words and
   hazard tape. Never use "cold", "frozen" or "ice" for anything that works.
3. **Width is temperature.** Set the words in the cut of their state: cold (condensed)
   for Asleep, installing and cold chapters; warm for Ready, Updating, Restart needed and
   Pairing; hot (expanded) for Streaming and the loudest headline. Faults take the warm cut:
   they are off the scale, and they must be read first.
4. **Lead with the verb, keep it short.** A state line that leads with a verb keeps the
   verb big and drops the rest to a second, smaller line: "Streaming / to Living room TV",
   "Restart / to finish", "Updating / to 20261003.0915".
5. **Say how it comes back.** Anything that goes away (a restart, power off, sleep) says
   how it returns and that the page reconnects by itself.
6. **No gamer talk, no hype.** No "beast", "insane", "next-level", no exclamation marks.
   "Turn it into a console tonight." is the loudest line on the site; nothing is louder.

## Examples from the prototypes

**Website headlines** (may change voice):
- Eyebrow: "A Steam streaming OS for your gaming PC"
- H1: "Leave the heat in the other room."
- "Every screen gets its own picture."
- "Three steps. The last one is on your phone."
- "Updates that undo themselves."
- "Off when nobody plays. On when you do."
- "Turn it into a console tonight."

**Control center hero** (word, then detail):
- Ready: "Ready / to stream" · "Open Moonlight on any device and pick this PC."
- Streaming: "Streaming / to Living room TV" · strip "Living room TV · 3840 × 2160 · 120 Hz · HDR · 38 min"
- Updating: "Updating / to 20261003.0915" · "Downloading 42 %"
- Restart needed: "Restart / to finish" · "Version 20261003.0915 is ready. Restart to finish."
- Fault: "No supported / graphics card" · "VaporOS is running, but streaming needs an AMD Radeon graphics card."

**Scenes** (may change voice):
- Restarting: "Restarting" · "VaporOS is restarting. This page reconnects by itself."
- Asleep: "Asleep" · "VaporOS powered off at 23:41, after 15 minutes without anyone playing.
  Wake it from Moonlight, or press its power button. This page reconnects by itself."
- The Wake card on the Asleep scene and on System › Power (user decision NEW-1):
  "Moonlight wakes it: open Moonlight and pick this PC." Then the wired adapter's MAC
  address and the subnet broadcast address, each with a Copy button, for any Wake-on-LAN
  app or router. While the PC is up: "Save these now, so you can wake it when this page
  can't reach it."

**Never voiced** (plain, always):
- Error: "Couldn't save the name. The name can use letters, digits and hyphens."
- Confirm: "Go back to 20260927.1900?" · "VaporOS starts the previous version from the next
  restart on. Your games and settings stay." · Cancel / Roll back
- Hold hint: "Keep holding Restart until the key fills."

## Signage words

The HUD and the TV's scale label print the state in lower-case mono. They come from
`state.*.signage` in `tokens.json`: ready, streaming, updating, restart needed, asleep,
fault, setting up (installing) and pairing. The chip labels are sentence case
(`state.*.label`): Ready, Streaming, Updating, Restart needed, Asleep, Needs attention,
Installing, Pairing.
