import Foundation

/// Replaces id-like path segments with `:id` so API events group by route:
/// `/api/trips/8412/photos/3f2a9c1e-...` becomes `/api/trips/:id/photos/:id`.
enum Paths {
    static func normalize(_ path: String) -> String {
        if path.isEmpty { return "/" }
        return path.split(separator: "/", omittingEmptySubsequences: false)
            .map { isID($0) ? ":id" : String($0) }
            .joined(separator: "/")
    }

    private static func isID(_ segment: Substring) -> Bool {
        let chars = Array(segment.utf8)
        guard !chars.isEmpty else { return false }
        if chars.allSatisfy(isDigit) { return true }
        if isUUID(segment) { return true }
        if chars.count >= 16, chars.allSatisfy(isHex) { return true }
        // Long opaque tokens (ULIDs, base64 ids) have digits; long words and slugs usually don't.
        return chars.count >= 20 && chars.allSatisfy(isToken) && chars.contains(where: isDigit)
    }

    private static func isUUID(_ s: Substring) -> Bool {
        let parts = s.split(separator: "-", omittingEmptySubsequences: false)
        guard parts.map(\.utf8.count) == [8, 4, 4, 4, 12] else { return false }
        return parts.allSatisfy { $0.utf8.allSatisfy(isHex) }
    }

    private static func isDigit(_ c: UInt8) -> Bool { c >= 48 && c <= 57 }

    private static func isHex(_ c: UInt8) -> Bool {
        isDigit(c) || (c >= 97 && c <= 102) || (c >= 65 && c <= 70)
    }

    private static func isToken(_ c: UInt8) -> Bool {
        isDigit(c) || (c >= 97 && c <= 122) || (c >= 65 && c <= 90) || c == 95 || c == 45
    }
}
