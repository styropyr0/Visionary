import { api } from "./api";

export const REPO = "https://github.com/styropyr0/Visionary";

export interface UpstreamRelease {
  tag: string;
  date?: string;
  url: string;
}

export interface Deployment {
  service: string;
  version: string;
  deployed_at: string;
  instances: number;
  sdk: string;
  runtime: string;
}

export interface ProjectInfo {
  version: string;
  commit: string;
  repo: string;
  latest?: string;
  update_available?: boolean;
  releases?: UpstreamRelease[];
  releases_error?: string;
  /** The first check of GitHub has not finished yet. */
  releases_pending?: boolean;
  deployments: Deployment[];
}

export const getProject = (signal?: AbortSignal) => api<ProjectInfo>("/api/project", { signal });

/** What to show for the running build: its tag, else its short commit. */
export function versionLabel(p: ProjectInfo | undefined): string {
  if (!p) return "";
  if (p.version) return p.version.startsWith("v") ? p.version : `v${p.version}`;
  if (p.commit) return p.commit.slice(0, 7);
  return "dev";
}

export interface Snippet {
  /** Shown above the code. */
  label: string;
  code: string;
}

export interface Integration {
  key: string;
  name: string;
  /** How it reaches Visionary, in one line. */
  how: string;
  steps: Snippet[];
  /** The SDK's own guide in the repository. */
  guide: string;
}

const guide = (dir: string) => `${REPO}/tree/main/sdks/${dir}#readme`;

/**
 * Setup for each platform. Server SDKs write straight to the database this
 * server reads; browser and mobile apps send to this server with a client key;
 * anything instrumented with OpenTelemetry sends OTLP with an ingest key.
 */
export function integrations(origin: string, latest: string): Integration[] {
  // The newest release, else the running one; Go can always ask for @latest.
  const tag = latest || "<release tag>";
  const goTag = latest || "latest";
  const db = `# The same database this Visionary server reads
VISIONARY_DATABASE_URL=postgres://user:pass@host:5432/app
# or, for Cloudflare D1: D1_ACCOUNT_ID, D1_DATABASE_ID, D1_API_TOKEN
VISIONARY_SERVICE=my-api`;
  return [
    {
      key: "go",
      name: "Go",
      how: "Middleware in your server writes requests, metrics and errors to the database.",
      guide: guide("go"),
      steps: [
        { label: "Install", code: `go get github.com/styropyr0/visionary@${goTag}` },
        { label: "Environment", code: db },
        {
          label: "Wrap your handler",
          code: `import visionary "github.com/styropyr0/visionary/sdks/go"

agent, _ := visionary.New(visionary.FromEnv()) // nil agent = collection off
agent.Start(ctx)
defer agent.Close(context.Background())

http.ListenAndServe(":8080", agent.Middleware(mux))`,
        },
      ],
    },
    {
      key: "python",
      name: "Python",
      how: "Django, FastAPI or Flask middleware writes to the database.",
      guide: guide("python"),
      steps: [
        { label: "Install from the repository", code: `pip install "visionary-sdk @ git+${REPO}#subdirectory=sdks/python"` },
        { label: "Environment", code: db },
        {
          label: "Django settings.py",
          code: `MIDDLEWARE = [
    "visionary.integrations.django.VisionaryMiddleware",  # first
    # ...
]`,
        },
      ],
    },
    {
      key: "node",
      name: "Node.js",
      how: "Express middleware writes to the database.",
      guide: guide("node"),
      steps: [
        { label: "Install from the repository", code: `git clone ${REPO}.git\nnpm install ./Visionary/sdks/node` },
        { label: "Environment", code: db },
        {
          label: "Express",
          code: `import visionary from "visionary-sdk";

visionary.init();
app.use(visionary.express());
// ... routes ...
app.use(visionary.expressErrorHandler());`,
        },
      ],
    },
    {
      key: "java",
      name: "Spring Boot",
      how: "A starter that configures itself and writes to the database.",
      guide: guide("java"),
      steps: [
        { label: "Build into your local Maven repository", code: `git clone ${REPO}.git\ncd Visionary/sdks/java && ./mvnw install` },
        { label: "Gradle", code: `implementation("io.github.styropyr0:visionary-spring-boot-starter:0.1.0-SNAPSHOT")` },
        { label: "Environment", code: db },
      ],
    },
    {
      key: "ruby",
      name: "Ruby",
      how: "Rack middleware for Rails and Sinatra writes to the database.",
      guide: guide("ruby"),
      steps: [
        { label: "Gemfile", code: `gem "visionary-sdk", require: "visionary",\n    git: "${REPO}", glob: "sdks/ruby/*.gemspec"` },
        { label: "Environment", code: db },
      ],
    },
    {
      key: "dotnet",
      name: ".NET",
      how: "ASP.NET Core middleware writes to the database.",
      guide: guide("dotnet"),
      steps: [
        {
          label: "Pack from the repository",
          code: `git clone ${REPO}.git
cd Visionary/sdks/dotnet/Visionary.AspNetCore
dotnet pack -c Release -o ~/local-packages
dotnet nuget add source ~/local-packages --name visionary-local
dotnet add package Visionary.AspNetCore`,
        },
        { label: "Program.cs", code: `builder.Services.AddVisionary();\nvar app = builder.Build();\napp.UseVisionary(); // first` },
        { label: "Environment", code: db },
      ],
    },
    {
      key: "browser",
      name: "Browser",
      how: "Sends page loads, Web Vitals, API calls and errors to this server.",
      guide: guide("browser"),
      steps: [
        { label: "Install from the repository", code: `git clone ${REPO}.git\nnpm install ./Visionary/sdks/browser` },
        {
          label: "Start it",
          code: `import Visionary from "visionary-browser";

Visionary.init({
  endpoint: "${origin}",
  key: "<client key from Settings → API keys>",
});`,
        },
      ],
    },
    {
      key: "android",
      name: "Android",
      how: "Sends screen loads, API timings and crashes to this server.",
      guide: guide("android"),
      steps: [
        { label: "settings.gradle.kts", code: `repositories { maven("https://jitpack.io") }` },
        { label: "app/build.gradle.kts", code: `implementation("com.github.styropyr0:Visionary:${tag}")` },
        {
          label: "Application.onCreate",
          code: `Visionary.init(
    this,
    VisionaryConfig(
        endpoint = "${origin}",
        key = "<client key from Settings → API keys>",
    ),
)`,
        },
        { label: "OkHttp (API timings)", code: `OkHttpClient.Builder().addInterceptor(VisionaryInterceptor())` },
      ],
    },
    {
      key: "ios",
      name: "iOS",
      how: "Sends screen loads, API timings and crashes to this server.",
      guide: guide("ios"),
      steps: [
        { label: "Swift Package Manager (local path)", code: `git clone ${REPO}.git\n// Package.swift\n.package(path: "../Visionary/sdks/ios")` },
        {
          label: "App start",
          code: `import Visionary

Visionary.start(config: VisionaryConfig(
    endpoint: URL(string: "${origin}")!,
    key: "<client key from Settings → API keys>"
))`,
        },
      ],
    },
    {
      key: "otel",
      name: "OpenTelemetry",
      how: "Any OpenTelemetry SDK or collector sends OTLP over HTTP to this server.",
      guide: `${REPO}/blob/main/docs/otlp.md`,
      steps: [
        {
          label: "Exporter environment",
          code: `OTEL_EXPORTER_OTLP_ENDPOINT=${origin}
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_HEADERS=x-visionary-key=<server ingest key>
OTEL_SERVICE_NAME=my-api`,
        },
      ],
    },
  ];
}
