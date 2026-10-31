# frozen_string_literal: true

require "minitest/autorun"
require "tmpdir"
require "fileutils"
require "visionary"

# The fixture text is the Go SDK's (sdks/go/hoststats/testdata/linux)
class SystemStatsTest < Minitest::Test
  HostStats = Visionary::System::HostStats

  PRESSURE = {
    "proc/pressure/cpu" => "some avg10=12.50 avg60=8.25 avg300=3.10 total=123456789\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
    "proc/pressure/memory" => "some avg10=4.20 avg60=2.10 avg300=0.80 total=9876543\nfull avg10=1.50 avg60=0.75 avg300=0.20 total=1234567\n",
    "proc/pressure/io" => "some avg10=30.00 avg60=22.40 avg300=10.00 total=55555555\nfull avg10=18.75 avg60=12.00 avg300=5.00 total=44444444\n",
    "cgroup/cpu.pressure" => "some avg10=55.10 avg60=40.00 avg300=20.00 total=1000\nfull avg10=50.00 avg60=35.00 avg300=18.00 total=900\n",
    "cgroup/memory.pressure" => "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
    "cgroup/io.pressure" => "some avg10=1.00 avg60=0.50 avg300=0.10 total=10\nfull avg10=0.90 avg60=0.40 avg300=0.10 total=9\n"
  }.freeze

  VMSTAT = <<~TXT
    nr_free_pages 1234567
    nr_dirty 512
    pgpgin 104857600
    pgpgout 209715200
    pswpin 1000
    pswpout 2000
    pgfault 50000000
    pgmajfault 20000
    pgsteal_kswapd 30000
    pgsteal_direct 5000
    pgsteal_khugepaged 0
    pgsteal_anon 1000
    pgsteal_file 34000
    pgscan_kswapd 60000
    pgscan_direct 9000
    pgscan_khugepaged 0
    pgscan_direct_throttle 7
    pgscan_anon 2000
    pgscan_file 67000
    oom_kill 3
  TXT

  MEMINFO = <<~TXT
    MemTotal:       16303300 kB
    MemFree:         1048576 kB
    MemAvailable:    8388608 kB
    Buffers:          204800 kB
    Cached:          4194304 kB
    SwapCached:            0 kB
    Dirty:             10240 kB
    Writeback:          2048 kB
    Shmem:            524288 kB
    Slab:             819200 kB
    SReclaimable:     614400 kB
    CommitLimit:    12345678 kB
    Committed_AS:   20971520 kB
    HugePages_Total:       0
  TXT

  def setup
    @root = Dir.mktmpdir
    PRESSURE.merge("proc/vmstat" => VMSTAT, "proc/meminfo" => MEMINFO).each { |rel, text| write(rel, text) }
  end

  def teardown
    FileUtils.rm_rf(@root)
  end

  def write(rel, text)
    path = File.join(@root, rel)
    FileUtils.mkdir_p(File.dirname(path))
    File.write(path, text)
  end

  def replace(rel, old, new)
    path = File.join(@root, rel)
    text = File.read(path)
    assert_includes text, old
    File.write(path, text.sub(old, new))
  end

  def test_parsers
    p = HostStats.parse_pressure(PRESSURE["proc/pressure/memory"])
    assert_equal [4.2, 2.1], p["some"]
    assert_equal 1.5, p["full"][0]
    assert_nil HostStats.parse_pressure("garbage\n")

    vm = HostStats.parse_vmstat(VMSTAT)
    assert_equal({ pgfault: 50_000_000, pgmajfault: 20_000, pswpin: 1000, pswpout: 2000, pgscan: 69_000, pgsteal: 35_000, oom_kill: 3 },
                 vm.slice(:pgfault, :pgmajfault, :pswpin, :pswpout, :pgscan, :pgsteal, :oom_kill))

    mi = HostStats.parse_meminfo(MEMINFO)
    assert_equal [10_240, 2048, 819_200, 524_288, 20_971_520, 12_345_678], mi.values_at("Dirty", "Writeback", "Slab", "Shmem", "Committed_AS", "CommitLimit")
    assert_nil HostStats.parse_meminfo("MemTotal: 1 kB\n")

    assert_equal [900, 12], HostStats.parse_cgroup_mem_stat("anon 123\npgfault 900\npgmajfault 12\n")
    assert_equal [800, 7], HostStats.parse_cgroup_mem_stat("cache 1\npgfault 5\ntotal_pgfault 800\ntotal_pgmajfault 7\n")
    assert_equal [14, 3], HostStats.parse_cgroup_mem_events("low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n")
  end

  def test_host
    s = HostStats.new(root: @root, linux: true)
    s.prime
    replace("proc/vmstat", "pgfault 50000000", "pgfault 50030000")
    replace("proc/vmstat", "pgmajfault 20000", "pgmajfault 20150")
    replace("proc/vmstat", "pswpin 1000", "pswpin 1300")
    replace("proc/vmstat", "pgscan_kswapd 60000", "pgscan_kswapd 61500")
    replace("proc/vmstat", "oom_kill 3", "oom_kill 4")
    v = {}
    s.sample(v, 15)
    want = {
      "psi_cpu_some_avg10" => 12.5, "psi_cpu_some_avg60" => 8.25, "psi_mem_some_avg10" => 4.2, "psi_mem_some_avg60" => 2.1, "psi_mem_full_avg10" => 1.5,
      "psi_io_some_avg10" => 30.0, "psi_io_some_avg60" => 22.4, "psi_io_full_avg10" => 18.75,
      "minor_faults_ps" => 1990.0, "major_faults_ps" => 10.0, "swap_in_ps" => 20.0, "swap_out_ps" => 0.0, "pages_scanned_ps" => 100.0, "pages_stolen_ps" => 0.0,
      "host_oom_kills" => 1,
      "mem_dirty_mb" => 10.0, "mem_writeback_mb" => 2.0, "mem_slab_mb" => 800.0, "mem_shmem_mb" => 512.0, "mem_committed_mb" => 20_480.0, "mem_commit_limit_mb" => 12_056.3
    }
    assert_equal want, v
  end

  def test_container_pressure_comes_from_the_cgroup
    s = HostStats.new(root: @root, linux: true, container: true, cgroup_dir: File.join(@root, "cgroup"))
    s.prime
    v = {}
    s.sample(v, 15)
    assert_equal 55.1, v["psi_cpu_some_avg10"]
    assert_equal 0.0, v["psi_mem_some_avg10"]
    assert_equal 0.9, v["psi_io_full_avg10"]

    s = HostStats.new(root: @root, linux: true, container: true)
    s.prime
    v = {}
    s.sample(v, 15)
    refute v.key?("psi_cpu_some_avg10"), "a container without a cgroup v2 directory read the host's pressure"
  end

  def test_off_linux_reads_nothing
    s = HostStats.new(root: @root, linux: false)
    s.prime
    v = {}
    s.sample(v, 15)
    assert_empty v
  end

  def test_cgroup_faults_and_memory_events
    write("proc/self/cgroup", "0::/\n")
    write("sys/fs/cgroup/cpu.stat", "usage_usec 1000\nnr_periods 0\nnr_throttled 0\n")
    write("sys/fs/cgroup/cpu.max", "max 100000\n")
    write("sys/fs/cgroup/memory.max", "max\n")
    write("sys/fs/cgroup/memory.current", "1048576\n")
    write("sys/fs/cgroup/memory.stat", "anon 123\npgfault 900\npgmajfault 12\n")
    write("sys/fs/cgroup/memory.events", "low 0\nhigh 14\nmax 3\noom 1\noom_kill 1\n")
    c = Visionary::System::Cgroup.detect(root: @root, env: { "KUBERNETES_SERVICE_HOST" => "10.0.0.1" })
    assert c.v2?
    assert c.in_container?
    c.prime
    c.instance_variable_set(:@last, c.instance_variable_get(:@last) - 10)
    write("sys/fs/cgroup/memory.stat", "anon 123\npgfault 1900\npgmajfault 112\n")
    write("sys/fs/cgroup/memory.events", "low 0\nhigh 20\nmax 4\noom 1\noom_kill 1\n")
    v = {}
    c.sample(v)
    assert_in_delta 90.0, v["container_minor_faults_ps"], 0.5
    assert_in_delta 10.0, v["container_major_faults_ps"], 0.1
    assert_equal 6, v["container_mem_high_events"]
    assert_equal 1, v["container_mem_max_events"]
  end

  class D1Store
    def dialect = "d1"
    def string_params? = true
  end

  def test_d1_splits_the_row
    rec = Visionary::System::Recorder.new(Visionary::Config.new, "inst", D1Store.new, nil, Visionary::System::HttpCounters.new, nil)
    cols = Visionary::Schema::TABLES["system_metrics"]
    assert_operator cols.size, :>, 100
    stmts = rec.send(:row_stmts, (0...cols.size).to_a)
    assert_equal 2, stmts.size
    assert_equal 100, stmts[0][1].size
    assert_equal 100, stmts[0][0].scan("NULLIF(?, '')").size
    assert_equal (100...cols.size).to_a, stmts[1][1]
    assert stmts[1][0].start_with?("UPDATE system_metrics SET #{cols[100]} = NULLIF(?, '')")
    assert stmts[1][0].end_with?("WHERE id = last_insert_rowid()")
  end
end
