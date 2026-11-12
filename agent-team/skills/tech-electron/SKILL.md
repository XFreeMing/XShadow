---
name: tech-electron
description: Implement Electron lifecycle, process bridges, operating-system integration and desktop packaging.
---

Identify the supported Electron version, main/preload/renderer boundaries and target operating systems. Trace startup, window restoration, IPC ownership, child-process lifecycle and quit behavior in the real application. Preserve the target's context-isolation, sandbox and permission model; expose only the bridge surface required for the task.
Test the desktop path affected by the change, including process termination or packaging where relevant. A browser-renderer pass alone cannot establish native bridge, menu, tray, file-dialog or installer correctness. Record which OS and artifact were actually exercised. Tauri and native platform implementations require their own specialist.
