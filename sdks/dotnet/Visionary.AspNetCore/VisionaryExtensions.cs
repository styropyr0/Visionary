using System;
using System.Linq;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.DependencyInjection.Extensions;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;
using Styropyr0.Visionary;

namespace Microsoft.Extensions.DependencyInjection
{
    /// <summary>Registers Visionary's collector.</summary>
    public static class VisionaryServiceCollectionExtensions
    {
        /// <summary>
        /// Adds request logging, system metrics, spans and error capture. Settings come from the
        /// VISIONARY_* variables (environment or configuration keys of the same names), then
        /// <paramref name="configure"/>. Pair it with <c>app.UseVisionary()</c>.
        /// </summary>
        public static IServiceCollection AddVisionary(this IServiceCollection services, Action<VisionaryOptions>? configure = null)
        {
            ArgumentNullException.ThrowIfNull(services);
            services.AddOptions<VisionaryOptions>().Configure<IServiceProvider>((o, sp) => o.Load(sp.GetService<IConfiguration>()));
            if (configure is not null)
            {
                services.Configure(configure);
            }
            services.TryAddSingleton<VisionaryAgent>();
            // Listeners first, so spans from other hosted services' startup work are seen too.
            services.TryAddEnumerable(ServiceDescriptor.Singleton<IHostedService, SpanListeners>());
            services.TryAddEnumerable(ServiceDescriptor.Singleton<IHostedService, TelemetryWriter>());
            services.TryAddEnumerable(ServiceDescriptor.Singleton<IHostedService, SystemSampler>());
            return services;
        }
    }
}

namespace Microsoft.Extensions.Logging
{
    using Microsoft.Extensions.DependencyInjection;

    /// <summary>Registers Visionary's log capture on a logging builder.</summary>
    public static class VisionaryLoggingBuilderExtensions
    {
        /// <summary>
        /// Copies what the application logs through <c>ILogger</c> into Visionary's app_logs. Capture
        /// is opt-in: <c>AddVisionary()</c> alone adds no provider. Other providers' output does not
        /// change. It also adds the collector (<c>AddVisionary()</c>), so it works on its own.
        /// </summary>
        public static ILoggingBuilder AddVisionary(this ILoggingBuilder builder, Action<VisionaryOptions>? configure = null)
        {
            ArgumentNullException.ThrowIfNull(builder);
            var services = builder.Services;
            services.AddVisionary(configure);
            if (services.Any(d => d.ServiceType == typeof(ILoggerProvider) && d.ImplementationType == typeof(VisionaryLoggerProvider)))
            {
                return builder;
            }
            services.Configure<VisionaryOptions>(o => o.LogsAdded = true);
            services.TryAddEnumerable(ServiceDescriptor.Singleton<IHostedService, AppLogWriter>());
            services.AddSingleton<ILoggerProvider, VisionaryLoggerProvider>();
            // A rule for this provider only, letting it see Trace and up whatever Logging:LogLevel
            // says; the console and every other provider keep their levels, and the provider
            // itself passes on what Settings → Logs can't keep. Inserted first, so a
            // Logging:Visionary rule from configuration, as specific, still wins over it.
            services.Configure<LoggerFilterOptions>(o =>
                o.Rules.Insert(0, new LoggerFilterRule(typeof(VisionaryLoggerProvider).FullName, null, LogLevel.Trace, null)));
            return builder;
        }
    }
}

namespace Microsoft.AspNetCore.Builder
{
    using Microsoft.Extensions.DependencyInjection;

    /// <summary>Adds Visionary's request middleware.</summary>
    public static class VisionaryApplicationBuilderExtensions
    {
        private const string Added = "__visionary.middleware";

        /// <summary>
        /// Logs every request and records unhandled exceptions. Call it first, before exception
        /// handlers, authentication and routing, so the log has the status the client got.
        /// With no database configured it adds nothing.
        /// </summary>
        public static IApplicationBuilder UseVisionary(this IApplicationBuilder app)
        {
            ArgumentNullException.ThrowIfNull(app);
            var agent = app.ApplicationServices.GetService<VisionaryAgent>()
                ?? throw new InvalidOperationException("visionary: call builder.Services.AddVisionary() before app.UseVisionary()");
            if (!agent.Enabled || app.Properties.ContainsKey(Added))
            {
                return app;
            }
            app.Properties[Added] = true;
            agent.Logger.LogDebug("visionary: request middleware added");
            return app.UseMiddleware<VisionaryMiddleware>();
        }
    }
}
