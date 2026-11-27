# Bot Main pixel comparison

These captures compare the NSGo implementation at `7663aa02` with the production React screen from `390a6ed6`. Both clients show the same isolated Dex, the same three demo Bots, and the same completed `faux/echo` turn. They do not use private data. See the [capture command](../../README.md#run-a-native-acceptance-check) and the [design contract](../../../../docs/dev/ui-design.md#pr-acceptance).

## Web

Each folder contains `react.png`, `nsgo.png`, `diff.png`, and `metrics.json`. Pink marks an unmatched pixel in a checked region. Amber marks another strong change. Blue marks a smaller change. Open the original images at full size. A small percentage for the whole image does not mean that a control passed.

- **1280 × 800, sidebar open:** [light](desktop-open-light/), [dark](desktop-open-dark/).
- **1280 × 800, sidebar closed:** [light](desktop-closed-light/), [dark](desktop-closed-dark/).
- **390 × 820, drawer closed:** [light](phone-closed-light/), [dark](phone-closed-dark/).
- **390 × 820, drawer open:** [light](phone-open-light/), [dark](phone-open-dark/).

The composer card, input, and Send control have the same measured edges in all six cases where the composer is exposed. The user bubble starts 1.375 CSS px above the reference. The limit is 1 CSS px. Search still fails its paint check in both open-drawer cases. **None of the eight cases passes all checks.**

## Native

The native images show the app window in light mode. The macOS window is 1280 × 800 pt at 2×. The iPhone 14 simulator window is 390 × 844 pt at 3×, with 47 pt above and 34 pt below the content. Do not compare device pixels directly with CSS pixels. Both native clients have a 98 pt composer card, a 44 pt input, and a 28 × 28 pt Send control. These measurements do not certify native pixel equality, dark mode, the software keyboard, or a physical iPhone.

- macOS: [one completed turn](native/macos-short.png), [empty Bot](native/macos-empty.png).
- iOS Simulator: [one completed turn](native/ios-short.png), [empty Bot](native/ios-empty.png), [navigation drawer](native/ios-drawer.png).
