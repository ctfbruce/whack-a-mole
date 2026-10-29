"""Small bursts separated by pauses; not an imitation of a particular website."""


def next_request(rng, index):
    pause = rng.uniform(6, 12) if index % 5 == 0 else rng.uniform(0.2, 0.5)
    return pause, rng.randint(64, 1024), rng.randint(8192, 65536)
