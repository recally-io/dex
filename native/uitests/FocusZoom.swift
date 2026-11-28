import XCTest

extension AppleSmoke {
    func checkChatViewport(in host: XCUIApplication, dismissBeforeSend: Bool) {
        let window = host.windows.firstMatch
        let web = host.webViews.containing(.textView, identifier: "Message Dex QA").firstMatch
        let composer = web.textViews["Message Dex QA"].firstMatch
        let send = web.buttons["Send message"].firstMatch
        let keyboard = host.keyboards.firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 15), host.debugDescription)
        let laidOut = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
            composer.isHittable && !composer.frame.isEmpty && send.exists && !send.frame.isEmpty
        }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [laidOut], timeout: 10), .completed)
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 5))
        let controls = [("input", composer, composer.frame), ("send", send, send.frame)]

        func checkBounds(_ phase: String) {
            for (name, control, baseline) in controls {
                let frame = control.frame
                print("Viewport \(phase) \(name): window=\(window.frame) baseline=\(baseline) current=\(frame)")
                XCTAssertFalse(frame.isEmpty, phase)
                XCTAssertGreaterThanOrEqual(frame.minX, window.frame.minX - 1, phase)
                XCTAssertLessThanOrEqual(frame.maxX, window.frame.maxX + 1, phase)
                XCTAssertEqual(frame.minX, baseline.minX, accuracy: 2, "\(phase): left edge moved")
                XCTAssertEqual(frame.maxX, baseline.maxX, accuracy: 2, "\(phase): right edge moved")
            }
        }

        checkBounds("Before typing")
        composer.tap()
        let tutorial = host.buttons["Continue"].firstMatch
        if tutorial.exists && tutorial.isHittable { tutorial.tap() }
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5))
        let message = "viewport\(UUID().uuidString.prefix(6).lowercased())"
        composer.typeText(message)
        XCTAssertTrue(keyboard.exists)
        checkBounds("Typing")
        XCTAssertTrue(send.isEnabled && send.isHittable)
        if dismissBeforeSend {
            window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.22)).tap()
            XCTAssertTrue(keyboard.waitForNonExistence(timeout: 5))
            checkBounds("Keyboard dismissed")
            XCTAssertTrue(web.otherElements["Bot menu: Dex QA"].firstMatch.isHittable)
        }
        send.tap()
        XCTAssertTrue(web.staticTexts["echo: " + message].firstMatch.waitForExistence(timeout: 15))
        XCTAssertTrue(keyboard.waitForNonExistence(timeout: 5))
        checkBounds("Reply received")
    }
}
