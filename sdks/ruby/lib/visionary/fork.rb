# frozen_string_literal: true

module Visionary
  # Process._fork (Ruby 3.1+) runs for every fork: Kernel#fork, Process.fork
  # and the forks Puma and Unicorn make for their workers. Only the child
  # restarts Visionary's threads.
  module ForkHook
    def _fork
      pid = super
      Visionary.after_fork if pid.zero?
      pid
    end
  end

  Process.singleton_class.prepend(ForkHook) if Process.respond_to?(:_fork)
end
