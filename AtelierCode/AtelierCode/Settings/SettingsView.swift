import SwiftUI

struct SettingsView: View {
    @Environment(AppModel.self) private var appModel

    var body: some View {
        @Bindable var settings = appModel.settings
        Form {
            Section {
                TextField("Server URL", text: $settings.serverURLString, prompt: Text(AppSettings.defaultServerURLString))
                    .autocorrectionDisabled()
                if appModel.settings.serverURL == nil {
                    Text("Enter a full URL including scheme, e.g. http://127.0.0.1:7331")
                        .font(.caption)
                        .foregroundStyle(.red)
                }
            } footer: {
                Text("Direct over Tailscale is the target setup. Use http://127.0.0.1:7331 with an SSH tunnel as fallback.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Section {
                SecureField("API Token (optional)", text: $settings.token)
            } footer: {
                Text("Sent as a bearer token. Leave empty unless the server sets COCKPIT_API_TOKEN.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
        .frame(width: 480)
        .fixedSize(horizontal: false, vertical: true)
        .onChange(of: appModel.settings.serverURLString) { appModel.rebuildClient() }
        .onChange(of: appModel.settings.token) { appModel.rebuildClient() }
    }
}
