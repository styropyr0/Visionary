# frozen_string_literal: true

module Visionary
  # Rails integration: inserts the middleware first in the stack, records
  # database spans from ActiveRecord, sees exceptions Rails turns into error
  # pages, and reads route patterns from Rails routing. Configure in an
  # initializer with Visionary.configure.
  class Railtie < ::Rails::Railtie
    initializer "visionary.middleware" do |app|
      app.middleware.insert_before 0, Visionary::Middleware
    end

    initializer "visionary.active_record" do
      ActiveSupport.on_load(:active_record) do
        Visionary::Telemetry::ActiveRecordHook.install if Visionary.config.spans
      end
    end

    # ShowExceptions and DebugExceptions rescue errors below the middleware;
    # the interceptor hands each one to it (it is recorded when the response
    # is a 5xx, so RecordNotFound turned into a 404 is not an error row).
    initializer "visionary.exceptions" do
      ActiveSupport.on_load(:action_controller) do
        if ActionDispatch::DebugExceptions.respond_to?(:register_interceptor)
          ActionDispatch::DebugExceptions.register_interceptor do |request, exception|
            request.set_header("visionary.exception", exception)
          end
        end
      end
    end

    config.after_initialize do
      cfg = Visionary.config
      cfg.logger ||= Rails.logger
      # Rails 7.1+ leaves the pattern in the env (request.route_uri_pattern);
      # older versions are asked to recognize the request again.
      cfg.route ||= Visionary::Railtie.method(:recognize) unless ActionDispatch::Request.method_defined?(:route_uri_pattern)
    end

    def self.recognize(env)
      req = ActionDispatch::Request.new(env)
      Rails.application.routes.router.recognize(req) { |route, _| return route.path.spec.to_s }
      ""
    rescue StandardError
      ""
    end
  end
end
