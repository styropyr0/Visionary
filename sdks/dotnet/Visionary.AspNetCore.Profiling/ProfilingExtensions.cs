using System;
using Microsoft.Extensions.DependencyInjection.Extensions;
using Microsoft.Extensions.Hosting;
using Styropyr0.Visionary;
using Styropyr0.Visionary.Profiling;

namespace Microsoft.Extensions.DependencyInjection
{
    /// <summary>Registers Visionary's CPU profiler.</summary>
    public static class VisionaryProfilingServiceCollectionExtensions
    {
        /// <summary>
        /// Lets the Visionary dashboard take CPU profiles of this service (Settings, Profiling).
        /// Nothing is sampled until profiling is turned on there; VISIONARY_PROFILE=false (or
        /// <see cref="VisionaryOptions.Profile"/> = false) turns it off for good. Call it after
        /// <c>AddVisionary()</c>.
        /// </summary>
        public static IServiceCollection AddVisionaryProfiling(this IServiceCollection services)
        {
            ArgumentNullException.ThrowIfNull(services);
            services.Configure<VisionaryOptions>(o => o.ProfilingAdded = true);
            services.TryAddEnumerable(ServiceDescriptor.Singleton<IHostedService, ProfilerService>());
            return services;
        }
    }
}
