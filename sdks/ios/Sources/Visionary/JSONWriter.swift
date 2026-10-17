import Foundation

/// A flat attribute value: the ingest API accepts strings and numbers only.
public enum AttributeValue: Equatable {
    case string(String)
    case number(Double)
}

extension AttributeValue: ExpressibleByStringLiteral, ExpressibleByIntegerLiteral, ExpressibleByFloatLiteral {
    public init(stringLiteral value: String) { self = .string(value) }
    public init(integerLiteral value: Int) { self = .number(Double(value)) }
    public init(floatLiteral value: Double) { self = .number(value) }
}

/// A minimal JSON object writer with stable number formatting (JSONSerialization prints
/// some doubles with 17 digits and escapes every `/`).
final class JSONWriter {
    private var out = "{"
    private var empty = true

    private static let maxAttrs = 50

    @discardableResult
    func string(_ key: String, _ value: String?) -> JSONWriter {
        guard let value else { return self }
        self.key(key)
        JSONWriter.quote(value, into: &out)
        return self
    }

    @discardableResult
    func number(_ key: String, _ value: Double?) -> JSONWriter {
        guard let value, let text = JSONWriter.number(value) else { return self }
        self.key(key)
        out += text
        return self
    }

    @discardableResult
    func attrs(_ key: String, _ attrs: [(String, AttributeValue)]) -> JSONWriter {
        let inner = JSONWriter()
        for (k, v) in attrs.prefix(JSONWriter.maxAttrs) {
            switch v {
            case .string(let s): inner.string(String(k.prefix(100)), String(s.prefix(1000)))
            case .number(let n): inner.number(String(k.prefix(100)), n)
            }
        }
        if !inner.empty { raw(key, inner.build()) }
        return self
    }

    @discardableResult
    func raw(_ key: String, _ json: String) -> JSONWriter {
        self.key(key)
        out += json
        return self
    }

    func build() -> String { out + "}" }

    private func key(_ key: String) {
        if !empty { out += "," }
        empty = false
        JSONWriter.quote(key, into: &out)
        out += ":"
    }

    static func quote(_ s: String, into out: inout String) {
        out += "\""
        for scalar in s.unicodeScalars {
            switch scalar {
            case "\"": out += "\\\""
            case "\\": out += "\\\\"
            case "\n": out += "\\n"
            case "\r": out += "\\r"
            case "\t": out += "\\t"
            default:
                if scalar.value < 0x20 || scalar.value == 0x2028 || scalar.value == 0x2029 {
                    let hex = String(scalar.value, radix: 16)
                    out += "\\u" + String(repeating: "0", count: 4 - hex.count) + hex
                } else {
                    out.unicodeScalars.append(scalar)
                }
            }
        }
        out += "\""
    }

    static func number(_ d: Double) -> String? {
        guard d.isFinite else { return nil }
        if d == d.rounded(), abs(d) < 1e15 { return String(Int64(d)) }
        return "\(d)"
    }
}
