import SwiftUI

extension View {
    /// The standard failure alert: presents while `error` holds a message
    /// and clears it on dismiss.
    func actionErrorAlert(
        _ error: Binding<String?>,
        title: String = "Action Failed"
    ) -> some View {
        alert(title, isPresented: Binding(
            get: { error.wrappedValue != nil },
            set: { if !$0 { error.wrappedValue = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(error.wrappedValue ?? "")
        }
    }
}
