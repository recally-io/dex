import XCTest

final class MacSmoke: XCTestCase {
    func testUserRetriesTheLocalServerAndGetsAnEcho() throws {
        continueAfterFailure = false
        let plist = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "Fixture", withExtension: "plist"))
        let fixture = try PropertyListDecoder().decode([String: String].self, from: Data(contentsOf: plist))
        let app = XCUIApplication(url: URL(fileURLWithPath: try XCTUnwrap(fixture["app"])))
        let home = try XCTUnwrap(fixture["home"])
        let gate = URL(fileURLWithPath: try XCTUnwrap(fixture["gate"]))
        app.launchEnvironment["HOME"] = home
        app.launchEnvironment["CFFIXED_USER_HOME"] = home
        app.launch()
        defer { app.terminate() }

        let retry = app.descendants(matching: .any)["Retry"].firstMatch
        XCTAssertTrue(retry.waitForExistence(timeout: 15))
        XCTAssertFalse(app.webViews.firstMatch.exists)
        capture(app, "01-mac-offline")
        FileHandle.standardError.write(Data("DEX_MAC_QA_OFFLINE_CONFIRMED\n".utf8))

        let ready = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
            FileManager.default.fileExists(atPath: gate.appendingPathComponent("start-default").path)
        }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [ready], timeout: 15), .completed)
        retry.click()

        let composer = app.webViews.descendants(matching: .any)["Message Dex QA"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 15))
        XCTAssertFalse(app.webViews.staticTexts["Sign in to dex"].exists)
        composer.click()
        let message = "retry"
        composer.typeText(message)
        XCTAssertEqual(composer.value as? String, message)
        composer.typeKey(.return, modifierFlags: [])
        let echo = "echo: " + message
        let answer = app.webViews.staticTexts.matching(NSPredicate(format: "label == %@ OR value == %@", echo, echo)).firstMatch
        XCTAssertTrue(answer.waitForExistence(timeout: 15), app.webViews.firstMatch.debugDescription)
        capture(app, "02-mac-echo")
    }

    private func capture(_ app: XCUIApplication, _ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
