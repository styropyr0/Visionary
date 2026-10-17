import Foundation
#if canImport(UIKit)
import UIKit
#endif

enum DeviceInfo {
    /// The hardware identifier, e.g. `iPhone15,2`; on the simulator, the simulated device's.
    static func model() -> String {
        if let simulated = ProcessInfo.processInfo.environment["SIMULATOR_MODEL_IDENTIFIER"] {
            return simulated
        }
        var info = utsname()
        uname(&info)
        let machine = withUnsafeBytes(of: &info.machine) { raw in
            String(decoding: raw.prefix { $0 != 0 }, as: UTF8.self)
        }
        return machine.isEmpty ? "unknown" : machine
    }

    /// E.g. `iOS 26.1`. Reads `UIDevice`, so call it from the main thread.
    static func os() -> String {
        #if canImport(UIKit) && !os(watchOS)
        let device = UIDevice.current
        return "\(device.systemName) \(device.systemVersion)"
        #else
        let v = ProcessInfo.processInfo.operatingSystemVersion
        #if os(macOS)
        let name = "macOS"
        #elseif os(Linux)
        let name = "Linux"
        #else
        let name = "unknown"
        #endif
        return "\(name) \(v.majorVersion).\(v.minorVersion)" + (v.patchVersion > 0 ? ".\(v.patchVersion)" : "")
        #endif
    }

    static func bundleString(_ key: String) -> String? {
        (Bundle.main.object(forInfoDictionaryKey: key) as? String).flatMap { $0.isEmpty ? nil : $0 }
    }
}
