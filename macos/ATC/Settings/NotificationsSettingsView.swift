import AppKit
import SwiftUI
import UserNotifications

/// The Notifications settings section: one switch, plus the one failure mode
/// worth surfacing — notifications turned off for atc at the OS level, where
/// every banner would be dropped silently. Authorization is requested when the
/// switch is turned on, so a fresh install never sees an unprompted system
/// dialog, and status is read when the tab appears and when the switch is
/// flipped rather than polled.
struct NotificationsSettingsView: View {
    @AppStorage(ThreadNotifier.preferenceKey) private var isEnabled = false
    @State private var isDenied = false

    var body: some View {
        Form {
            Section {
                Toggle("Notify me when a thread finishes or needs input", isOn: $isEnabled)
            } footer: {
                Text("Banners appear only while atc is in the background, and come down once you have seen the thread — from any client.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if isEnabled, isDenied {
                Section {
                    Label(
                        "Notifications are turned off for atc in System Settings.",
                        systemImage: "exclamationmark.triangle.fill"
                    )
                    .font(.caption)
                    .foregroundStyle(.orange)
                    Button("Open System Settings") { openSystemSettings() }
                }
            }
        }
        .formStyle(.grouped)
        .task { await readAuthorization() }
        .onChange(of: isEnabled) { _, enabled in
            Task {
                if enabled {
                    _ = try? await UNUserNotificationCenter.current()
                        .requestAuthorization(options: [.alert, .sound])
                }
                await readAuthorization()
            }
        }
    }

    private func readAuthorization() async {
        isDenied = await UNUserNotificationCenter.current()
            .notificationSettings().authorizationStatus == .denied
    }

    private func openSystemSettings() {
        let bundleID = Bundle.main.bundleIdentifier ?? ""
        guard let url = URL(
            string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension?id=\(bundleID)"
        ) else { return }
        NSWorkspace.shared.open(url)
    }
}

#if DEBUG

#Preview("Notifications") {
    NotificationsSettingsView()
        .frame(width: 700, height: 450)
        .preferredColorScheme(.dark)
}

#endif
