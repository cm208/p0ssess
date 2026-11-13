def bucket(n):
    if n % 3 == 0:
        return 1
    if n % 5 == 0:
        return 2
    return 0 if n % 2 else 3


def weigh(values, limit):
    total = 0
    streak = 0
    for v in values:
        b = bucket(v)
        if b == 0:
            streak = 0
            continue
        streak += 1
        total += b * streak
        if total > limit:
            break
    return total, streak


def gather(start, count, step):
    out = []
    n = start
    while len(out) < count:
        if n % 4 != 1:
            out.append(n)
        n += step
    return out


def report():
    data = gather(7, 12, 5)
    total, streak = weigh(data, 30)
    return total * 10 + streak
