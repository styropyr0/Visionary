# frozen_string_literal: true

module Visionary
  # Copies up to max bytes of a body as it streams past, and counts all of it.
  class Capture
    attr_reader :size

    def initialize(max, enabled)
      @max = max
      @enabled = enabled
      @buf = enabled ? String.new(capacity: [max, 1024].min, encoding: Encoding::BINARY) : nil
      @size = 0
    end

    def feed(chunk, at = @size)
      n = chunk.bytesize
      if @enabled && at == @buf.bytesize && at < @max
        @buf << chunk.byteslice(0, @max - at).b
      end
      @size = at + n if at + n > @size
    end

    def bytes = @buf || ""
    def truncated? = @enabled && @size > @max
  end

  # Wraps rack.input: the app reads as before, and the first max bytes it
  # reads are kept for the log.
  class InputProxy
    def initialize(input, capture)
      @input = input
      @capture = capture
      @pos = 0
    end

    def read(*args)
      data = @input.read(*args)
      observe(data)
      data
    end

    def gets(*args)
      observe(@input.gets(*args))
    end

    def each
      return enum_for(:each) unless block_given?
      @input.each { |line| yield observe(line) }
    end

    def rewind
      @pos = 0
      @input.rewind
    end

    def close = @input.respond_to?(:close) ? @input.close : nil

    def respond_to_missing?(name, include_all = false) = @input.respond_to?(name, include_all)

    def method_missing(name, *args, &block)
      @input.respond_to?(name) ? @input.public_send(name, *args, &block) : super
    end

    private

    def observe(data)
      if data.is_a?(String) && !data.empty?
        @capture.feed(data, @pos)
        @pos += data.bytesize
      end
      data
    end
  end

  # Wraps a Rack response body. Chunks pass through untouched (streaming
  # still streams); a capped copy is kept, and done is called once when the
  # server closes the body.
  class BodyProxy
    def initialize(body, capture, &done)
      @body = body
      @capture = capture
      @done = done
    end

    def each(&block)
      return enum_for(:each) unless block
      @body.each do |chunk|
        @capture.feed(chunk.to_s)
        yield chunk
      end
    end

    # Rack 3: a body with to_ary must close itself when it is called.
    def to_ary
      arr = @body.to_ary
      arr.each { |chunk| @capture.feed(chunk.to_s) }
      arr
    ensure
      close
    end

    # Rack 3 streaming bodies.
    def call(stream)
      @body.call(StreamProxy.new(stream, @capture))
    ensure
      finish
    end

    def to_path = @body.to_path

    def respond_to?(name, include_all = false)
      case name.to_sym
      when :to_ary, :to_path, :call, :each then @body.respond_to?(name, include_all)
      else super
      end
    end

    def close
      @body.close if @body.respond_to?(:close) && !@closed
    ensure
      @closed = true
      finish
    end

    private

    def finish
      done = @done
      @done = nil
      done&.call
    end

    def respond_to_missing?(name, include_all = false) = @body.respond_to?(name, include_all)

    def method_missing(name, *args, &block)
      @body.respond_to?(name) ? @body.public_send(name, *args, &block) : super
    end
  end

  class StreamProxy
    def initialize(stream, capture)
      @stream = stream
      @capture = capture
    end

    def write(data)
      @capture.feed(data.to_s)
      @stream.write(data)
    end

    def <<(data)
      write(data)
      self
    end

    def respond_to_missing?(name, include_all = false) = @stream.respond_to?(name, include_all)

    def method_missing(name, *args, &block)
      @stream.respond_to?(name) ? @stream.public_send(name, *args, &block) : super
    end
  end
end
