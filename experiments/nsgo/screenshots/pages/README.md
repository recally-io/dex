# Bot and workspace pages

The images in [pairs](pairs) show React on the left and NSGo Web on the right. Both clients used the same temporary Dex: one Bot, one Task, a session with an echo reply, one disabled Automation, and one artifact. No production data was used.

## Web

- Artifacts: [desktop](pairs/artifacts-desktop.png) · [phone](pairs/artifacts-phone.png). The dialog stays on the session URL.
- New session: [desktop entry](pairs/new-session-desktop.png) · [phone menu](pairs/new-session-phone.png). The read-only comparison does not create a session. The separate opt-in check creates one, checks the project dialog, and verifies that Main has not changed.
- Sessions: [desktop](pairs/sessions-desktop.png) · [phone](pairs/sessions-phone.png).
- Automations: [desktop](pairs/automations-desktop.png) · [phone](pairs/automations-phone.png).
- Bot settings: [desktop](pairs/bot-settings-desktop.png) · [phone](pairs/bot-settings-phone.png).
- Board: [desktop](pairs/board-desktop.png) · [phone](pairs/board-phone.png).
- Integrations: [desktop](pairs/integrations-desktop.png) · [phone](pairs/integrations-phone.png).
- Settings: [desktop](pairs/settings-desktop.png) · [phone](pairs/settings-phone.png).

See the [original PNGs](originals), [per-page checks and coordinates](metrics.json), and [reproduction steps](../../README.md#run-a-native-acceptance-check). Chrome used 1280 × 800 and 390 × 820 CSS pixels, light appearance, and device scale 1. The test compares the positions of visible titles and primary controls. It does not compare every painted pixel.

59 of 60 measured coordinates met the 1 CSS px limit. In the desktop Artifacts dialog with one file, the Close button was 1.09375 px below the reference. The phone view passed this check. The page-entry checks do not measure detail layouts: for example, the Task detail composer was approximately 78 px higher on desktop and 84 px higher on the phone than in React.

## Native

The [macOS](macos) and [iOS Simulator](ios) directories contain actual app captures. Both platforms passed seven flows from the workspace controls: open and preview an artifact, read a session, open an Automation and its edit form, switch Bot settings tabs, open a Task, open the MCP form, and open the provider form. Neither run saved credentials or used an external service. These results do not cover a physical iPhone, dark appearance, native OAuth, or complete pixel parity.
