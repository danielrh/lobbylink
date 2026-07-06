// Segment.java — one link of a worm's body, which trails the segment ahead of
// it by a fixed distance. This is the whole "worm follows a lead point" trick.
//
// Adapted from anderrh/wildlifejava (BSD 2-Clause):
//   Copyright (c) 2026, anderrh   (see LICENSE in this directory)
// The class is unchanged; only the file's original StdDraw-based `main` demo
// was removed, since this example draws with MiniDraw instead.

public class Segment {
  public double radius;
  public double x;
  public double y;
  public double size;
  public Segment(double x, double y, double size){
    this.x = x;
    this.y = y;
    this.size = size;
    this.radius = 2;
  }
  public void attach(Segment segment){
    if (this.x == segment.x && this.y == segment.y) {
      this.x += 1;
    }
    double distance = this.dist(segment);
    this.x=(segment.x-this.x)*(distance-segment.radius)/distance+this.x;
    this.y=(segment.y-this.y)*(distance-segment.radius)/distance+this.y;
  }
  public double dist(Segment segment){
    return Math.sqrt(Math.abs(this.x-segment.x)*Math.abs(this.x-segment.x)+Math.abs(this.y-segment.y)*Math.abs(this.y-segment.y));
  }
}
