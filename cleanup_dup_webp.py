import os

target_folder = r"/home/dburman/nightsky/output"

for root, dirs, files in os.walk(target_folder):
    # Find all base names of PNG files in current directory
    png_names = {os.path.splitext(f)[0] for f in files if f.lower().endswith('.png')}
    
    # Check WebP files and delete if base name exists in png_names
    for f in files:
        if f.lower().endswith('.webp'):
            base_name = os.path.splitext(f)[0]
            if base_name in png_names:
                file_path = os.path.join(root, f)
                os.remove(file_path)
                print(f"Deleted: {file_path}")
