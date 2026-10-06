#!/usr/bin/env python3
"""Author unencrypted Blu-ray regression images with tsMuxeR, FFmpeg and genisoimage."""
import argparse
from pathlib import Path
import struct
import subprocess
import shutil


def add_second_angle(data):
    data = bytearray(data)
    start = struct.unpack_from(">I", data, 8)[0]
    count = struct.unpack_from(">H", data, start + 6)[0]
    position = start + 10
    inserted = 0
    for _ in range(count):
        length = struct.unpack_from(">H", data, position)[0]
        item = position + 2
        data[item + 10] |= 0x10
        # A separately authored alternate scene has matching timestamps and its own CLPI.
        alternate = f"{int(data[item:item + 5]) + 100:05d}".encode()
        angle = bytes([2, 0]) + alternate + data[item + 5:item + 9] + data[item + 11:item + 12]
        data[item + 32:item + 32] = angle
        struct.pack_into(">H", data, position, length + len(angle))
        inserted += len(angle)
        position += 2 + length + len(angle)
    struct.pack_into(">I", data, start, struct.unpack_from(">I", data, start)[0] + inserted)
    for field in (12, 16):
        offset = struct.unpack_from(">I", data, field)[0]
        if offset:
            struct.pack_into(">I", data, field, offset + inserted)
    return bytes(data)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("output", type=Path)
    parser.add_argument("--tsmuxer", default="tsMuxeR")
    args = parser.parse_args()
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=True)
    video = root / "source.mkv"
    subprocess.run([
        "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24",
        "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "12",
        "-c:v", "libx264", "-preset", "ultrafast", "-profile:v", "high", "-level:v", "4.1",
        "-pix_fmt", "yuv420p", "-x264-params", "bluray-compat=1:keyint=24:scenecut=0",
        "-c:a", "ac3", "-b:a", "192k", str(video),
    ], check=True)
    meta = root / "disc.meta"
    meta.write_text('MUXOPT --blu-ray --split-duration=6s\nV_MPEG4/ISO/AVC, "' + str(video) +
                    '", track=1\nA_AC3, "' + str(video) + '", track=2\n', encoding="utf-8")
    disc = root / "authored"
    subprocess.run([args.tsmuxer, str(meta), str(disc)], check=True)
    # tsMuxeR writes a Blu-ray UDF 2.50 image when the destination is an ISO.
    subprocess.run([args.tsmuxer, str(meta), str(root / "native-udf250.iso")], check=True)
    alternate = root / "alternate.mkv"
    subprocess.run([
        "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=blue:size=1280x720:rate=24",
        "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000", "-t", "12",
        "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
        "-x264-params", "bluray-compat=1:keyint=24:scenecut=0", "-c:a", "ac3",
        "-b:a", "192k", str(alternate),
    ], check=True)
    alternate_meta = root / "alternate.meta"
    alternate_meta.write_text(meta.read_text(encoding="utf-8").replace(str(video), str(alternate)).replace(
        "--blu-ray", "--blu-ray --m2tsOffset=100 --mplsOffset=100"), encoding="utf-8")
    alternate_disc = root / "alternate-authored"
    subprocess.run([args.tsmuxer, str(alternate_meta), str(alternate_disc)], check=True)
    for directory in ("STREAM", "CLIPINF"):
        for file in (alternate_disc / "BDMV" / directory).iterdir():
            shutil.copy2(file, disc / "BDMV" / directory / file.name)
    for file in (disc / "BDMV" / "PLAYLIST").glob("*.mpls"):
        file.write_bytes(add_second_angle(file.read_bytes()))
    subprocess.run(["genisoimage", "-quiet", "-udf", "-o", str(root / "native-angles.iso"), str(disc)], check=True)
    print("Native fixtures: " + str(root))


if __name__ == "__main__":
    main()
