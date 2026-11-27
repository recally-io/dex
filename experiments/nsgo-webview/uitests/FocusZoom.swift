import XCTest

final class FocusZoom: XCTestCase {
    func testUserCanTypeAndSendWithoutZooming() throws {
        continueAfterFailure = true
        let app = XCUIApplication(bundleIdentifier: "dev.vaayne.dex.webviewpoc")
        app.launch()
        let window = app.windows.firstMatch
        let web = app.webViews.firstMatch
        let composer = web.textViews["Message Dex"].firstMatch
        let send = web.buttons["Send message"].firstMatch
        let menu = web.otherElements["Bot menu: Dex"].firstMatch
        let keyboard = app.keyboards.firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 15))
        XCTAssertTrue(send.exists)
        XCTAssertTrue(menu.exists)
        XCTAssertFalse(keyboard.exists)

        func capture(_ phase: String) {
            let menuFrame = menu.exists ? menu.frame : .null
            let keyboardFrame = keyboard.exists ? keyboard.frame : .null
            print("ZOOM \(phase) window=\(window.frame) web=\(web.frame) composer=\(composer.frame) send=\(send.frame) sendHittable=\(send.isHittable) sendEnabled=\(send.isEnabled) menu=\(menuFrame) keyboard=\(keyboard.exists) keyboardFrame=\(keyboardFrame)")
            let photo = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
            photo.name = phase
            photo.lifetime = .keepAlways
            add(photo)
        }

        let baseline = composer.frame
        let sendBaseline = send.frame
        func checkGeometry(_ phase: String) {
            let screen = window.frame.insetBy(dx: -1, dy: 0)
            for (name, frame, original) in [("composer", composer.frame, baseline), ("send", send.frame, sendBaseline)] {
                XCTAssertFalse(frame.isEmpty, "\(phase): \(name) has no frame")
                XCTAssertGreaterThanOrEqual(frame.minX, screen.minX, "\(phase): \(name) clips the left edge")
                XCTAssertLessThanOrEqual(frame.maxX, screen.maxX, "\(phase): \(name) clips the right edge")
                XCTAssertEqual(frame.minX, original.minX, accuracy: 2, "\(phase): \(name) left edge moved")
                XCTAssertEqual(frame.maxX, original.maxX, accuracy: 2, "\(phase): \(name) right edge moved")
            }
        }

        capture("baseline")
        composer.tap()
        // A new simulator can show the system keyboard tutorial on first focus.
        let tutorial = app.buttons["Continue"].firstMatch
        if tutorial.exists && tutorial.isHittable { tutorial.tap() }
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "A real tap must show the software keyboard")
        let marker = UUID().uuidString.prefix(6)
        let message = "Focus zoom QA \(marker)."
        composer.typeText(message)
        capture("before-send-keyboard")
        checkGeometry("keyboard shown")
        XCTAssertTrue(send.isEnabled && send.isHittable, "Send must be usable while the keyboard is shown")

        window.coordinate(withNormalizedOffset: CGVector(dx: 0.50, dy: 0.22)).tap()
        let hidden = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: keyboard)
        XCTAssertEqual(XCTWaiter.wait(for: [hidden], timeout: 5), .completed, "A tap outside the composer must hide the keyboard")
        capture("before-send-keyboard-hidden")
        checkGeometry("keyboard hidden")
        XCTAssertTrue(menu.isHittable, "The chat header must be reachable after dismissing the keyboard")
        XCTAssertTrue(send.isEnabled && send.isHittable, "Send must be usable after dismissing the keyboard")
        if send.isEnabled && send.isHittable { send.tap() }
        XCTAssertTrue(web.staticTexts["echo: " + message].firstMatch.waitForExistence(timeout: 12), "A tap on Send must produce a new echo in this chat")
        capture("after-send")
        checkGeometry("echo shown")
        XCTAssertFalse(keyboard.exists)
    }
}
