# frozen_string_literal: true

module Visionary
  # A bounded queue that never blocks producers: when full, push drops the
  # item and counts it. Consumers wait with a deadline (Thread::Queue#pop
  # gained a timeout only in Ruby 3.2).
  class BoundedQueue
    def initialize(max)
      @max = max
      @items = []
      @dropped = 0
      @closed = false
      @mutex = Mutex.new
      @cond = ConditionVariable.new
    end

    def push(item)
      @mutex.synchronize do
        if @items.size >= @max
          @dropped += 1
          return false
        end
        @items << item
        @cond.signal
        true
      end
    end

    # Up to n items, waiting until at least one is there, the deadline (a
    # monotonic time in seconds) passes, or the queue is closed.
    def take(n, deadline)
      @mutex.synchronize do
        while @items.empty? && !@closed
          left = deadline - Process.clock_gettime(Process::CLOCK_MONOTONIC)
          break if left <= 0
          @cond.wait(@mutex, left)
        end
        @items.shift(n)
      end
    end

    def size = @mutex.synchronize { @items.size }

    def take_dropped
      @mutex.synchronize { @dropped.tap { @dropped = 0 } }
    end

    def close
      @mutex.synchronize do
        @closed = true
        @cond.broadcast
      end
    end

    def closed? = @mutex.synchronize { @closed }
  end
end
