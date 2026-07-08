# Local Connections scope Cockpit Projects

AtelierCode represents each Cockpit server as a local Connection with an app-chosen name, URL, token, and stable local identity, while Projects and Terminal Sessions remain Cockpit-owned records. The app scopes displayed Projects and Terminal Sessions by the Connection they came from instead of trying to make Cockpit project or session IDs globally meaningful across servers; this lets multiple local or remote Cockpit servers appear together without adding cross-server identity to the Cockpit API.
