docker build -t ffmpeg-static-builder .

# Remove old container if it exists
docker rm temp_ffmpeg_container 2>/dev/null || true
# Create new container and copy out the updated binary
docker create --name temp_ffmpeg_container ffmpeg-static-builder
# Ensure the target bin/ directory exists
mkdir -p bin
docker cp temp_ffmpeg_container:/ffmpeg ./bin/ffmpeg
docker rm temp_ffmpeg_container
