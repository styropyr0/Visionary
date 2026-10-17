#if canImport(UIKit) && !os(watchOS)
import ObjectiveC
import UIKit

extension Visionary {
    /// Starts timing a screen. Call it from `viewDidLoad` (or `loadView`).
    public static func screenDidLoad(_ controller: UIViewController) {
        guard client != nil else { return }
        ScreenTiming.begin(controller)
    }

    /// Ends the timing started by `screenDidLoad(_:)` and records a `screen` event, once per
    /// controller. Call it from `viewDidAppear(_:)`.
    public static func screenDidAppear(_ controller: UIViewController, name: String? = nil) {
        guard let client else { return }
        let screenName = name ?? String(describing: type(of: controller))
        client.currentScreen = screenName
        guard let ms = ScreenTiming.end(controller) else { return }
        client.screen(screenName, durationMs: ms)
    }
}

/// Time from `viewDidLoad` to `viewDidAppear(_:)`: the screen's setup, first layout and draw,
/// and the presentation transition (so a pushed screen includes the push animation).
enum ScreenTiming {
    private static var startKey: UInt8 = 0
    private static var swizzled = false

    static func begin(_ controller: UIViewController) {
        objc_setAssociatedObject(controller, &startKey, NSNumber(value: CACurrentMediaTime()), .OBJC_ASSOCIATION_RETAIN_NONATOMIC)
    }

    static func end(_ controller: UIViewController) -> Double? {
        guard let start = objc_getAssociatedObject(controller, &startKey) as? NSNumber, start.doubleValue > 0 else {
            return nil
        }
        // Keep a marker so later appearances (returning to the screen) aren't reported again.
        objc_setAssociatedObject(controller, &startKey, NSNumber(value: 0), .OBJC_ASSOCIATION_RETAIN_NONATOMIC)
        return (CACurrentMediaTime() - start.doubleValue) * 1000
    }

    /// Swaps in timing versions of `viewDidLoad` and `viewDidAppear(_:)` for every view controller.
    /// Only controllers defined in the app's main bundle are recorded, not UIKit's own containers.
    static func swizzle() {
        guard Thread.isMainThread, !swizzled else { return }
        swizzled = true
        exchange(#selector(UIViewController.viewDidLoad), #selector(UIViewController.visionary_viewDidLoad))
        exchange(#selector(UIViewController.viewDidAppear(_:)), #selector(UIViewController.visionary_viewDidAppear(_:)))
    }

    static func isAppController(_ controller: UIViewController) -> Bool {
        Bundle(for: type(of: controller)) == Bundle.main
    }

    private static func exchange(_ original: Selector, _ replacement: Selector) {
        let cls: AnyClass = UIViewController.self
        guard let a = class_getInstanceMethod(cls, original), let b = class_getInstanceMethod(cls, replacement) else { return }
        method_exchangeImplementations(a, b)
    }
}

extension UIViewController {
    // After the exchange these names call the original UIKit implementations.
    @objc fileprivate func visionary_viewDidLoad() {
        if ScreenTiming.isAppController(self) { Visionary.screenDidLoad(self) }
        visionary_viewDidLoad()
    }

    @objc fileprivate func visionary_viewDidAppear(_ animated: Bool) {
        visionary_viewDidAppear(animated)
        if ScreenTiming.isAppController(self) { Visionary.screenDidAppear(self) }
    }
}
#endif
