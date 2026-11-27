# Dex UI design contract

This contract applies to the production React UI and the NSGo client on Web, macOS, and iOS. Dex is a workspace where the owner selects a Bot, reads its work, and decides what to do next. The content gets the strongest emphasis. Navigation stays quiet and predictable.

## Choose the reference before building

For a port, identify the **production route, component, and state** first. For example, use Bot Main with one completed turn and an empty draft, not just “chat.” Match its information order, actions, and reading order. Use [Web design tokens](../../web/src/styles.css) for values and the rendered React screen for composition. An NSGo default, an old mockup, and a screenshot of the port are not design references.

For a new screen, choose the nearest existing page pattern. Before implementation, specify the user's main action, its location at both widths, and the empty, pending, and failure states that apply. If the proposal changes the reference itself, identify it as a product design change in the PR. Do not update a screenshot or relax a tolerance to make an accidental difference pass.

## Visual language

- **Color.** Use `background` for the page, `sidebar` for navigation, and `card` for a contained surface. Use `foreground` for content and `muted-foreground` for secondary information. Use `primary` with `primary-foreground` for primary controls. In Dex, `accent` is a quiet interactive surface, not the color of the primary button. Resolve each role in light and dark mode. Do not use the platform's default blue as the Dex primary color.
- **Meaning.** Reserve color for Bot identity and meaningful status. Derive the Bot avatar tint from its ID as [BotAvatar](../../web/src/components/bot-avatar.tsx) does. Keep that mark the same in the sidebar and header. Pair status colors with a label or accessible name; a colored dot alone cannot explain an error.
- **Type.** Use the system sans face, including its CJK fallback. Use mono for code and identifiers. Base text is 14 px. Bot rows use 13.5 px, chat text 15 px, and secondary labels typically 12–13 px. Use the shared page heading hierarchy: 24 px for a global page, 18 px inside a Bot pane. Do not add another title if the same title is already in the header.
- **Spacing and shape.** Use the component's existing spacing before introducing a value. Common steps are 4, 8, 12, 16, 24, and 32 px. Named radii are 4 for chips, 6 for controls, 8 for cards, 12 for windows and the composer, and 18 for message bubbles. Component-specific geometry, such as the 9 px navigation row radius, stays local. Separate sections with a 1 px line or a change of surface. Reserve raised shadows for overlays; do not put every section in a raised card.
- **Controls.** Use the existing outline icon for the same action. Use a quiet button for navigation and secondary actions. Give an icon-only button an accessible name. Show which item is current and which action is unavailable. Never add an inactive look-alike for a feature that does not exist. In the same locale, use the same domain terms and action names.

## Screen patterns

### Workspace shell

The references are [Sidebar](../../web/src/components/sidebar.tsx), [Shell](../../web/src/components/shell.tsx), and [BotLayout](../../web/src/components/bot-layout.tsx). The [desktop reference](../../experiments/nsgo/screenshots/react-desktop.png) and [narrow reference](../../experiments/nsgo/screenshots/react-drawer.png) show the layout with public demo data.

- At widths **≥1024 px**, show a 260 px sidebar by default. Its 48 px top row contains the collapse control and Search. Only the middle list scrolls: live Bots in server order, then New bot. Pin Board, Integrations, and Settings to the bottom in that order. Bot rows are 36 px high; global links are 32 px high.
- At widths **<1024 px**, open that same hierarchy from the left in a modal drawer, at most 85% of the viewport width. Dim the content with the reference's 10% black overlay; blur the backdrop where supported. Selection, outside click, and Escape dismiss the drawer. Keyboard focus must return to a usable control.
- The Bot header is 48 px high. Center the Bot mark and title. Place session files and Bot actions on the right; on narrow screens group the Bot actions in Menu. A global route has no Bot actions. Do not promote sessions, files, or Bot settings into global navigation.

### Conversation

Use [Session](../../web/src/routes/session.tsx), [Transcript](../../web/src/components/bot/transcript.tsx), and [Composer](../../web/src/components/composer.tsx). These states have **different** layouts:

- **Empty and writable:** center the welcome text, composer, and available Bot summary as one group. **Short history:** align the transcript to the bottom of the available space above the composer. **Long history:** scroll the transcript independently. Keep the reader's position when they scroll up and provide a way back to the latest message.
- Align the user's bubble to the right and the assistant's unboxed text to the left. User bubble padding is 16 px horizontally and 10 px vertically. Assistant prose is at most 680 px wide. Preserve line breaks and wrap long text without horizontal page scrolling.
- Center the composer in a column up to 760 px wide. Leave 32 px at either side and 20 px below it at widths ≥640 px; below 640 px use 12 px side and bottom insets. A one-line empty field starts at 44 px. Extra lines can grow up to 200 px before the field scrolls. Do not fix the card height when a hint, file, or error is present.
- Keep Send in the lower right: a monochrome up arrow whose availability follows the draft. The enabled composer uses `foreground` for the button and `card` for the arrow. Follow the reference for Stop, busy, and read-only states. A read-only transcript has no writable composer. Keep recovery available after a connection failure. Routine connection status must not create a permanent toolbar between history and input.

### Board, settings, and other pages

Use [FramedPage, PanePage, and Section](../../web/src/components/page.tsx). Put the title and main action first, then the area's controls and sections. A single-column global page uses the narrow measure (816 px); a page that needs two columns can use 1120 px. Preserve the owning scope: shared integrations belong to the workspace, Bot integrations to the Bot. On a phone, stack the content in reading order rather than shrinking a desktop grid.

For forms and dialogs, keep labels visible, keep entered values after a recoverable error, and place the reason next to the field or action. A destructive action must retain the reference's confirmation. Pending, empty, read-only, and failed are different states, not four versions of an empty panel.

## Allowed platform differences

System font rasterization, text wrapping, keyboard, file picker, safe area, and native focus appearance may differ. Keep the same action, order, scope, and result. Respect reduced motion. Preserve visible keyboard focus and screen-reader names. Expand touch hit areas without moving the visible icons. Opening the software keyboard must leave the active field and submit action reachable. A narrow Chrome screenshot cannot establish iPhone keyboard or VoiceOver behavior.

## PR acceptance

1. Name the reference route, state, and changed pattern. Use the same isolated service, data, locale, theme, and display scale on both clients. Publish only synthetic Bot names and messages. Do not upload screenshots from a private workspace.
2. Compare **1280 × 800** with the sidebar open and **390 × 820** with the drawer closed and open as relevant. Check light and dark appearances for changes to tokens or controls. Match the semantic colors; do not fail a comparison for antialiasing or the system clock. Exercise the changed action and one relevant non-default state, such as offline recovery or a read-only session.
3. For shell and short-chat changes, run the [browser comparison](../../experiments/nsgo/sidebar-check.cjs). Compare the **gap from the last reply text box to the empty input field**, not an absolute Y coordinate. With one completed text turn, allow at most **12 px** difference from React; allow **4 px** for the empty field height and **1 channel value out of 255** for the reply text color and the enabled Send fill. The check types a temporary draft and never submits it. These are browser comparison tolerances, not extra padding or native font requirements.
4. Attach the paired screenshots, record the check result, and list untested platforms separately. A different action location, wrong navigation scope, clipped control, wrong theme role, or failed measurement blocks visual sign-off. For an intentional platform exception, record the affected component, platform constraint, user effect, and reviewer's disposition. A successful build does not waive a visual failure.
