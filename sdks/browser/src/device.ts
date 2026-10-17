/** "Chrome 141 on macOS" and "macOS" from the user agent, without a parser library. */
export function describeDevice(ua = navigator.userAgent): { device: string; os: string } {
  const m = (re: RegExp) => re.exec(ua);
  let browser = "Browser";
  let x: RegExpExecArray | null;
  if ((x = m(/Edg(?:e|A|iOS)?\/(\d+)/))) browser = `Edge ${x[1]}`;
  else if ((x = m(/(?:OPR|Opera)\/(\d+)/))) browser = `Opera ${x[1]}`;
  else if ((x = m(/SamsungBrowser\/(\d+)/))) browser = `Samsung Internet ${x[1]}`;
  else if ((x = m(/(?:Firefox|FxiOS)\/(\d+)/))) browser = `Firefox ${x[1]}`;
  else if ((x = m(/(?:Chrome|CriOS)\/(\d+)/))) browser = `Chrome ${x[1]}`;
  else if ((x = m(/Version\/(\d+)[\d.]*.*Safari\//))) browser = `Safari ${x[1]}`;

  let os = "Other";
  let family = os;
  if ((x = m(/Android (\d+(?:\.\d+)?)/))) [os, family] = [`Android ${x[1]}`, "Android"];
  else if ((x = m(/(?:iPhone|CPU) OS (\d+)_(\d+)/))) [os, family] = [`iOS ${x[1]}.${x[2]}`, /iPad/.test(ua) ? "iPadOS" : "iOS"];
  else if (/Windows/.test(ua)) os = family = "Windows";
  else if (/CrOS/.test(ua)) os = family = "ChromeOS";
  else if (/Mac OS X|Macintosh/.test(ua)) os = family = "macOS";
  else if (/Linux/.test(ua)) os = family = "Linux";
  return { device: `${browser} on ${family}`, os };
}
