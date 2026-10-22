import sys, os

def stat(pid):
    with open('/proc/%d/stat' % pid, 'rb') as f:
        s = f.read().decode('utf-8', 'replace')
    i = s.rindex(')')
    comm = s[s.index('(')+1:i]
    rest = s[i+2:].split()
    return comm, int(rest[1]), int(rest[2]), int(rest[3]), int(rest[4])  # ppid, pgid, sid, tty_nr

def children_map():
    m = {}
    for d in os.listdir('/proc'):
        if d.isdigit():
            try:
                c, pp, pg, sd, tt = stat(int(d))
                m.setdefault(pp, []).append(int(d))
            except OSError:
                pass
    return m

root = int(sys.argv[1])
m = children_map()

def walk(pid, depth=0):
    try:
        c, pp, pg, sd, tt = stat(pid)
    except OSError:
        print('%s%d GONE' % ('  ' * depth, pid))
        return
    print('%s%s(%d) ppid=%d pgid=%d sid=%d tty=%d' % ('  ' * depth, c, pid, pp, pg, sd, tt))
    for cp in sorted(m.get(pid, [])):
        walk(cp, depth + 1)

walk(root)
