import SwiftUI

extension Binding where Value == Bool {
    /// Item-driven presentation for alerts and confirmation dialogs that
    /// have no `item:` variant: true while `item` holds a value; dismissal
    /// clears it.
    init<Item>(presented item: Binding<Item?>) {
        self.init(
            get: { item.wrappedValue != nil },
            set: { if !$0 { item.wrappedValue = nil } }
        )
    }
}

extension View {
    /// The one Rename… alert: a name field, a Rename button disabled while
    /// the trimmed draft is empty, and Cancel. `perform` receives the item
    /// and the trimmed name.
    func renameAlert<Item>(
        _ title: String,
        item: Binding<Item?>,
        draft: Binding<String>,
        perform: @escaping (Item, String) -> Void
    ) -> some View {
        alert(title, isPresented: Binding(presented: item)) {
            TextField("Name", text: draft)
            Button("Rename") {
                if let value = item.wrappedValue {
                    perform(value, draft.wrappedValue.trimmingCharacters(in: .whitespaces))
                }
            }
            .disabled(draft.wrappedValue.trimmingCharacters(in: .whitespaces).isEmpty)
            Button("Cancel", role: .cancel) {}
        }
    }
}
