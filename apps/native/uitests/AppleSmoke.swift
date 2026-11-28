import UIKit
import XCTest

final class AppleSmoke: XCTestCase {
    private var app: XCUIApplication!
    private var fixture: [String: String]!

    override func setUpWithError() throws {
        continueAfterFailure = false
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "Fixture", withExtension: "plist"))
        fixture = try PropertyListDecoder().decode([String: String].self, from: Data(contentsOf: url))
        app = XCUIApplication(bundleIdentifier: try XCTUnwrap(fixture["bundle"]))
        app.launch()
    }

    private func wait(_ element: XCUIElement, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(element.waitForExistence(timeout: 15), "Expected \(element)", file: file, line: line)
    }

    private func control(_ title: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label == %@ OR identifier == %@", title, title)).firstMatch
    }

    private func capture(_ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    private func replace(_ field: XCUIElement, with text: String) {
        wait(field)
        field.tap()
        if let value = field.value as? String, value != field.placeholderValue, !value.isEmpty {
            field.press(forDuration: 0.8)
            let selectAll = app.descendants(matching: .any)["Select All"].firstMatch
            if selectAll.waitForExistence(timeout: 2) {
                selectAll.tap()
            } else {
                field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: value.count))
            }
        }
        field.typeText(text)
    }

    private var address: XCUIElement {
        app.textFields.matching(NSPredicate(format: "label == %@ OR placeholderValue == %@ OR identifier == %@", "Server address", "https://dex.example.com", "server-address")).firstMatch
    }

    private func connect(_ origin: String) {
        replace(address, with: origin)
        control("Connect").tap()
    }

    private var signInTitle: XCUIElement {
        app.webViews.staticTexts["Sign in to dex"].firstMatch
    }

    private func signIn(_ token: String) {
        wait(signInTitle)
        replace(app.webViews.secureTextFields.firstMatch, with: token)
        app.webViews.buttons["Sign in"].tap()
    }

    private var composer: XCUIElement {
        app.webViews.descendants(matching: .any).matching(NSPredicate(format: "label == %@ OR placeholderValue == %@ OR identifier == %@", "Message Dex QA", "Message Dex QA", "Message Dex QA")).firstMatch
    }

    private func send(_ message: String) {
        wait(composer)
        composer.tap()
        composer.typeText(message)
        let button = app.webViews.buttons["Send message"].firstMatch
        wait(button)
        button.tap()
        wait(reply(to: message))
    }

    private func reply(to message: String) -> XCUIElement {
        app.webViews.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "echo: " + message)).firstMatch
    }

    private struct Session: Decodable {
        let id: Int64
    }

    private struct History: Decodable {
        struct Item: Decodable {
            struct Attachment: Decodable {
                let id: String
                let mimeType: String
                let size: Int
            }
            let text: String?
            let model: String?
            let attachments: [Attachment]?
        }
        let items: [Item]
    }

    private func api(_ path: String, json: [String: String]? = nil) async throws -> Data {
        let origin = try XCTUnwrap(fixture["primary"])
        let token = try XCTUnwrap(fixture["token"])
        let url = try XCTUnwrap(URL(string: origin + "/api/bots/default" + path))
        var request = URLRequest(url: url, timeoutInterval: 5)
        request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        if let json {
            request.httpMethod = "POST"
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONEncoder().encode(json)
        }
        let (data, response) = try await URLSession.shared.data(for: request)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, json == nil ? 200 : 202)
        return data
    }

    private func hasCommittedEcho(_ message: String, session: Int64) async throws -> Bool {
        let data = try await api("/sessions/\(session)/history?before=999999999")
        let history = try JSONDecoder().decode(History.self, from: data)
        return history.items.contains { $0.model == "faux/echo" && $0.text == "echo: " + message }
    }

    // The empty workspace changes layout on its first message. Measure an existing chat.
    private func seedViewportChat() async throws -> String {
        let data = try await api("/sessions/main")
        let session = try JSONDecoder().decode(Session.self, from: data).id
        let marker = "baseline\(UUID().uuidString.prefix(6))"
        _ = try await api("/sessions/\(session)/messages", json: ["id": UUID().uuidString, "text": marker])
        return marker
    }

    @MainActor
    func testUserCanSendWithoutZoomingInDex() async throws {
        let marker = try await seedViewportChat()
        connect(try XCTUnwrap(fixture["primary"]))
        signIn(try XCTUnwrap(fixture["token"]))
        wait(reply(to: marker))
        checkChatViewport(in: app, dismissBeforeSend: true)
    }

    @MainActor
    func testUserCanSendWithoutZoomingInSafari() async throws {
        let marker = try await seedViewportChat()
        let origin = try XCTUnwrap(fixture["primary"])
        app = XCUIApplication(bundleIdentifier: "com.apple.mobilesafari")
        app.open(try XCTUnwrap(URL(string: origin + "/b/default")))
        wait(signInTitle)
        let tip = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "View Bookmarks, Share Menu")).firstMatch
        if tip.exists {
            app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.907, dy: 0.710)).tap()
        }
        signIn(try XCTUnwrap(fixture["token"]))
        wait(reply(to: marker))
        checkChatViewport(in: app, dismissBeforeSend: false)
    }

    func testUserCannotConnectToUntrustedTLS() throws {
        wait(control("Connect to Dex"))
        capture("01-first-launch")
        connect(try XCTUnwrap(fixture["primary"]))
        wait(control("Retry"))
        XCTAssertFalse(signInTitle.exists)
        capture("02-untrusted-tls")
        control("Retry").tap()
        wait(control("Retry"))
        XCTAssertFalse(signInTitle.exists)
    }

    @MainActor
    func testUserConnectsSignsInAndCatchesUpAfterBackground() async throws {
        connect(try XCTUnwrap(fixture["open"]))
        wait(control("Retry"))
        XCTAssertFalse(app.webViews.firstMatch.exists)
        capture("03-no-auth-rejected")
        connect(try XCTUnwrap(fixture["primary"]))
        wait(signInTitle)
        capture("04-sign-in")
        signIn("qa-invalid-token")
        wait(app.webViews.staticTexts.matching(NSPredicate(format: "label CONTAINS[c] %@", "token is not correct")).firstMatch)
        signIn(try XCTUnwrap(fixture["token"]))
        let marker = String(UUID().uuidString.prefix(8))
        let foreground = "Apple QA foreground " + marker
        send(foreground)
        capture("05-echo")
        let data = try await api("/sessions/main")
        let session = try JSONDecoder().decode(Session.self, from: data).id
        let sameSession = try await hasCommittedEcho(foreground, session: session)
        XCTAssertTrue(sameSession, "The UI must show the session that the QA client will write to.")

        XCUIDevice.shared.press(.home)
        XCTAssertTrue(app.wait(for: .runningBackground, timeout: 10) || app.state == .runningBackgroundSuspended)
        let background = "Apple QA produced in background " + marker
        _ = try await api("/sessions/\(session)/messages", json: [
            "id": UUID().uuidString.lowercased(), "text": background,
        ])
        let deadline = ContinuousClock.now + .seconds(20)
        var committed = false
        while ContinuousClock.now < deadline {
            committed = try await hasCommittedEcho(background, session: session)
            if committed { break }
            try await Task.sleep(for: .milliseconds(200))
        }
        XCTAssertTrue(committed, "The background answer must be committed before the app comes to the foreground.")
        XCTAssertTrue(app.state == .runningBackground || app.state == .runningBackgroundSuspended)
        app.activate()
        wait(reply(to: background))
        capture("06-background-catch-up")
    }

    private func switchWorkspaceFromSidebar() {
        XCTAssertFalse(control("Change address").exists, "Chat must not have a connection toolbar.")
        let navigation = app.webViews.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Open navigation")).firstMatch
        wait(navigation)
        navigation.tap()
        let switcher = app.webViews.links.matching(NSPredicate(format: "label BEGINSWITH %@", "Switch workspace")).firstMatch
        wait(switcher)
        capture("16-workspace-switcher")
        switcher.tap()
        wait(control("Switch Workspace"))
        wait(address)
    }

    func testUserSwitchesAndForgetsTheServer() throws {
        let primary = try XCTUnwrap(fixture["primary"])
        let alternate = try XCTUnwrap(fixture["alternate"])
        let token = try XCTUnwrap(fixture["token"])
        connect(primary)
        wait(signInTitle)
        app.webViews.links["Switch workspace"].tap()
        wait(control("Switch Workspace"))
        wait(address)
        capture("17-native-workspace-switcher")
        XCTAssertFalse(control("Retry").exists)
        control("Back to workspace").tap()
        signIn(token)
        wait(composer)
        app.terminate()
        app.launch()
        wait(composer)
        XCTAssertFalse(signInTitle.exists)

        switchWorkspaceFromSidebar()
        connect(alternate)
        wait(signInTitle)
        capture("07-switched-sign-in")
        signIn(token)
        wait(composer)
        switchWorkspaceFromSidebar()
        control("Forget server").tap()
        XCTAssertTrue(control("Forget server").waitForNonExistence(timeout: 15))
        wait(address)
        XCTAssertFalse((address.value as? String ?? "").contains(alternate))
        app.terminate()
        app.launch()
        wait(control("Connect to Dex"))
        capture("08-forgotten")
        connect(alternate)
        wait(signInTitle)
        capture("09-forgotten-sign-in")

        // An older server has no settings link. The system link must still work.
        app.terminate()
        app.open(try XCTUnwrap(URL(string: "dex-workspace://settings")))
        wait(address)
        XCTAssertEqual(address.value as? String, alternate)
    }

    @MainActor
    func testUserAttachesAndSendsAnImage() async throws {
        connect(try XCTUnwrap(fixture["primary"]))
        signIn(try XCTUnwrap(fixture["token"]))
        wait(composer)
        app.webViews.buttons["Attach files"].tap()
        capture("10-attachment-menu")
        let library = control("Photo Library")
        wait(library)
        library.tap()
        let photos = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH[c] %@", "Photo,"))
        let first = photos.matching(NSPredicate(format: "NOT label CONTAINS[c] %@", ", Photo,")).firstMatch
        if first.waitForExistence(timeout: 4), first.isHittable {
            first.tap()
        } else {
            // Some Photos builds expose the entire three-column grid as one element.
            let grid = photos.matching(NSPredicate(format: "label CONTAINS[c] %@", ", Photo,")).firstMatch
            wait(grid)
            capture("15-attachment-picker")
            let bounds = grid.frame
            let appBounds = app.frame
            let center = CGPoint(x: bounds.minX + bounds.width / 6, y: bounds.minY + bounds.width / 6)
            app.coordinate(withNormalizedOffset: .zero)
                .withOffset(CGVector(dx: center.x - appBounds.minX, dy: center.y - appBounds.minY)).tap()
        }
        let add = app.buttons.matching(NSPredicate(format: "label == %@ OR label == %@", "Add", "Done")).firstMatch
        if add.waitForExistence(timeout: 2) { add.tap() }
        wait(app.webViews.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Remove ")).firstMatch)
        capture("11-attachment-ready")

        let message = "Apple QA image " + String(UUID().uuidString.prefix(8))
        send(message)
        let data = try await api("/sessions/main")
        let session = try JSONDecoder().decode(Session.self, from: data).id
        let historyData = try await api("/sessions/\(session)/history?before=999999999")
        let history = try JSONDecoder().decode(History.self, from: historyData)
        let attachments = try XCTUnwrap(history.items.first { $0.text == message }?.attachments)
        XCTAssertEqual(attachments.count, 1)
        XCTAssertTrue(attachments.first?.mimeType.hasPrefix("image/") == true)
        let file = try XCTUnwrap(attachments.first)
        XCTAssertGreaterThan(file.size, 0)
        let uploaded = try await api("/uploads/\(file.id)")
        let image = try XCTUnwrap(UIImage(data: uploaded)?.cgImage)
        XCTAssertEqual(image.width, 2)
        XCTAssertEqual(image.height, 2)
        capture("12-attachment-sent")
    }

    func testUserSeesDifferentPromptsForLinksAndSilentOAuth() throws {
        connect(try XCTUnwrap(fixture["navigation"]))
        let link = app.webViews.links["Ordinary outside link"]
        wait(link)
        link.tap()
        let alert = app.alerts.firstMatch
        wait(alert.staticTexts["Open Link in Browser?"])
        wait(alert.buttons["Open Browser"])
        capture("13-external-link-confirm")
        alert.buttons["Cancel"].tap()

        app.webViews.buttons["Start scripted OAuth"].tap()
        wait(alert.staticTexts["Navigation Blocked"])
        wait(alert.staticTexts["To complete OAuth, open Dex in your browser and restart OAuth there."])
        wait(alert.buttons["Open Browser"])
        capture("14-oauth-open-dex-prompt")
        alert.buttons["Cancel"].tap()
        wait(link)
    }
}
