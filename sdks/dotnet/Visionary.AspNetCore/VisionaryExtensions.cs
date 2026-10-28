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
        /// Captures <c>ILogger</c> output into Visionary; other providers are unaffected.
        /// Also adds the collector, so it works without a separate <c>AddVisionary()</c>.
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
            // Let this provider see Trace and up whatever Logging:LogLevel says; it filters itself.
            // Inserted first so a Logging:Visionary rule from configuration still wins.
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
